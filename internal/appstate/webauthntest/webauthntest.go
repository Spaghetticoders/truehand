// Package webauthntest is a software authenticator for tests: it registers and signs like a
// phone's passkey would, with keys that exist only in memory.
package webauthntest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"strings"

	"github.com/spaghetticoders/truehand/internal/sign"
)

type Authenticator struct {
	ID     []byte
	RPID   string // the host alone
	Origin string // defaults to https://RPID; set it to add a port
	Key    crypto.Signer
	Count  uint32
	Flags  byte // defaults to UP|UV
}

func NewES256(rpID string) *Authenticator {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	return &Authenticator{ID: randID(), RPID: rpID, Key: k}
}

func NewRS256(rpID string) *Authenticator {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return &Authenticator{ID: randID(), RPID: rpID, Key: k}
}

func randID() []byte {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return b
}

func (a *Authenticator) CredentialID() string { return base64.RawURLEncoding.EncodeToString(a.ID) }

func (a *Authenticator) PublicKeyPEM() string {
	der, err := x509.MarshalPKIXPublicKey(a.Key.Public())
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func (a *Authenticator) host() string {
	host, _, err := sign.SplitRPID(a.RPID)
	if err != nil {
		panic(err)
	}
	return host
}

func (a *Authenticator) origin() string {
	if a.Origin != "" {
		return a.Origin
	}
	_, o, _ := sign.SplitRPID(a.RPID)
	return o
}

func (a *Authenticator) flags() byte {
	if a.Flags != 0 {
		return a.Flags
	}
	return 0x05
}

// Assert signs challenge as navigator.credentials.get would: authenticatorData, the
// clientDataJSON (both base64url) and the signature (standard base64, DER for ECDSA).
func (a *Authenticator) Assert(challenge []byte) (authData, clientData, sig string) {
	a.Count++
	rp := sha256.Sum256([]byte(a.host()))
	ad := append(rp[:], a.flags())
	ad = binary.BigEndian.AppendUint32(ad, a.Count)
	cdj, _ := json.Marshal(map[string]any{"type": "webauthn.get", "challenge": base64.RawURLEncoding.EncodeToString(challenge), "origin": a.origin(), "crossOrigin": false})
	cdh := sha256.Sum256(cdj)
	h := sha256.Sum256(append(append([]byte{}, ad...), cdh[:]...))
	var s []byte
	var err error
	switch k := a.Key.(type) {
	case *ecdsa.PrivateKey:
		s, err = ecdsa.SignASN1(rand.Reader, k, h[:])
	case *rsa.PrivateKey:
		s, err = rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, h[:])
	}
	if err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(ad), base64.RawURLEncoding.EncodeToString(cdj), base64.StdEncoding.EncodeToString(s)
}

// SignServer signs a server intent with this passkey; Credential and Server are filled in.
func (a *Authenticator) SignServer(in sign.ServerIntent) sign.SignedServer {
	in.Credential = a.CredentialID()
	if in.Server == "" {
		in.Server = a.RPID
	}
	ad, cd, sig := a.Assert(in.Challenge())
	return sign.SignedServer{Intent: in, Format: sign.FormatWebAuthn, Sig: sig, AuthenticatorData: ad, ClientDataJSON: cd}
}

// Create answers navigator.credentials.create with attestation "none": the
// attestationObject and the clientDataJSON, base64url.
func (a *Authenticator) Create(challenge []byte) (attestationObject, clientData string) {
	rp := sha256.Sum256([]byte(a.host()))
	ad := append(rp[:], a.flags()|0x40)
	ad = binary.BigEndian.AppendUint32(ad, a.Count)
	ad = append(ad, make([]byte, 16)...) // AAGUID
	ad = binary.BigEndian.AppendUint16(ad, uint16(len(a.ID)))
	ad = append(ad, a.ID...)
	ad = append(ad, a.coseKey()...)
	att := cborMap(
		cborText("fmt"), cborText("none"),
		cborText("attStmt"), []byte{0xa0},
		cborText("authData"), cborBytes(ad),
	)
	cdj, _ := json.Marshal(map[string]any{"type": "webauthn.create", "challenge": base64.RawURLEncoding.EncodeToString(challenge), "origin": a.origin(), "crossOrigin": false})
	return base64.RawURLEncoding.EncodeToString(att), base64.RawURLEncoding.EncodeToString(cdj)
}

func (a *Authenticator) coseKey() []byte {
	switch k := a.Key.(type) {
	case *ecdsa.PrivateKey:
		x, y := make([]byte, 32), make([]byte, 32)
		k.X.FillBytes(x)
		k.Y.FillBytes(y)
		return cborMap(cborInt(1), cborInt(2), cborInt(3), cborInt(-7), cborInt(-1), cborInt(1), cborInt(-2), cborBytes(x), cborInt(-3), cborBytes(y))
	case *rsa.PrivateKey:
		e := big(uint64(k.E))
		return cborMap(cborInt(1), cborInt(3), cborInt(3), cborInt(-257), cborInt(-1), cborBytes(k.N.Bytes()), cborInt(-2), cborBytes(e))
	}
	panic("unsupported key")
}

func big(v uint64) []byte {
	b := binary.BigEndian.AppendUint64(nil, v)
	return []byte(strings.TrimLeft(string(b), "\x00"))
}

func cborHead(major byte, n uint64) []byte {
	switch {
	case n < 24:
		return []byte{major<<5 | byte(n)}
	case n < 1<<8:
		return []byte{major<<5 | 24, byte(n)}
	case n < 1<<16:
		return binary.BigEndian.AppendUint16([]byte{major<<5 | 25}, uint16(n))
	default:
		return binary.BigEndian.AppendUint32([]byte{major<<5 | 26}, uint32(n))
	}
}

func cborInt(v int) []byte {
	if v >= 0 {
		return cborHead(0, uint64(v))
	}
	return cborHead(1, uint64(-1-v))
}

func cborBytes(b []byte) []byte { return append(cborHead(2, uint64(len(b))), b...) }
func cborText(s string) []byte  { return append(cborHead(3, uint64(len(s))), s...) }

func cborMap(kv ...[]byte) []byte {
	out := cborHead(5, uint64(len(kv)/2))
	for _, x := range kv {
		out = append(out, x...)
	}
	return out
}

// SignES256 signs a server intent's message directly, as the Mac app's Secure Enclave key does.
func SignES256(k *ecdsa.PrivateKey, in sign.ServerIntent) sign.SignedServer {
	h := sha256.Sum256(in.Message())
	s, err := ecdsa.SignASN1(rand.Reader, k, h[:])
	if err != nil {
		panic(err)
	}
	return sign.SignedServer{Intent: in, Format: sign.FormatES256, Sig: base64.StdEncoding.EncodeToString(s)}
}
