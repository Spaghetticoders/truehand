package sign

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
)

// COSE algorithms a passkey may register with.
const (
	AlgES256 = -7
	AlgRS256 = -257
)

// Credential is a passkey a registration created. PublicKey is its key as a PEM PUBLIC KEY,
// the form approvers list. The attestation is not checked: who may register is the
// registration's caller's to decide, not the authenticator's.
type Credential struct {
	ID             []byte
	PublicKey      string
	Alg            int
	SignCount      uint32
	AAGUID         [16]byte
	BackupEligible bool
	// BackedUp: the passkey is synced to the cloud of its platform (Google, iCloud...).
	BackedUp bool
}

const (
	maxAttestationObject = 64 << 10
	maxClientData        = 16 << 10
	maxCredentialID      = 1023
)

// VerifyRegistration checks a WebAuthn registration (navigator.credentials.create) made for
// rpID (a host name; the origin may use any https port) with this challenge, by a user present and
// verified, and returns the credential it created.
func VerifyRegistration(rpID string, challenge, clientDataJSON, attestationObject []byte) (*Credential, error) {
	return verifyRegistration(rpID, "", challenge, clientDataJSON, attestationObject)
}

func verifyRegistration(rpID, origin string, challenge, cdj, att []byte) (*Credential, error) {
	if err := CheckRPID(rpID); err != nil {
		return nil, err
	}
	if len(challenge) < 16 {
		return nil, errors.New("webauthn: challenge too short")
	}
	if len(cdj) > maxClientData || len(att) > maxAttestationObject {
		return nil, errors.New("webauthn: registration too large")
	}
	if err := checkClientData(cdj, "webauthn.create", challenge, rpID, origin); err != nil {
		return nil, err
	}
	authData, err := attestationAuthData(att)
	if err != nil {
		return nil, err
	}
	return parseAuthData(authData, rpID)
}

// attestationObject is a map {fmt: text, attStmt: map, authData: bytes}; only authData counts.
func attestationAuthData(att []byte) ([]byte, error) {
	obj, n, err := decodeCBOR(att)
	if err != nil {
		return nil, fmt.Errorf("webauthn: attestation object: %w", err)
	}
	if n != len(att) || obj.Kind != cborMap || !obj.uniqueKeys() {
		return nil, errors.New("webauthn: attestation object is not one CBOR map")
	}
	f, ok := obj.textKey("fmt")
	if !ok || f.Kind != cborText {
		return nil, errors.New("webauthn: attestation object has no fmt")
	}
	if st, ok := obj.textKey("attStmt"); !ok || st.Kind != cborMap {
		return nil, errors.New("webauthn: attestation object has no attStmt")
	}
	ad, ok := obj.textKey("authData")
	if !ok || ad.Kind != cborBytes {
		return nil, errors.New("webauthn: attestation object has no authData")
	}
	return ad.Bytes, nil
}

func parseAuthData(ad []byte, host string) (*Credential, error) {
	if len(ad) < 37 {
		return nil, errors.New("webauthn: authenticator data too short")
	}
	rp := sha256.Sum256([]byte(host))
	if !bytes.Equal(ad[:32], rp[:]) {
		return nil, errors.New("webauthn: registered for another relying party")
	}
	flags := ad[32]
	if flags&flagUP == 0 || flags&flagUV == 0 {
		return nil, errors.New("webauthn: the user was not present and verified")
	}
	if flags&flagAT == 0 {
		return nil, errors.New("webauthn: no attested credential data")
	}
	if flags&flagBS != 0 && flags&flagBE == 0 {
		return nil, errors.New("webauthn: backed up but not backup eligible")
	}
	c := &Credential{
		SignCount:      binary.BigEndian.Uint32(ad[33:37]),
		BackupEligible: flags&flagBE != 0,
		BackedUp:       flags&flagBS != 0,
	}
	rest := ad[37:]
	if len(rest) < 18 {
		return nil, errors.New("webauthn: attested credential data too short")
	}
	copy(c.AAGUID[:], rest[:16])
	idLen := int(binary.BigEndian.Uint16(rest[16:18]))
	rest = rest[18:]
	if idLen == 0 || idLen > maxCredentialID || idLen > len(rest) {
		return nil, errors.New("webauthn: bad credential id length")
	}
	c.ID = bytes.Clone(rest[:idLen])
	rest = rest[idLen:]
	key, n, err := decodeCBOR(rest)
	if err != nil {
		return nil, fmt.Errorf("webauthn: credential public key: %w", err)
	}
	rest = rest[n:]
	if flags&flagED != 0 {
		ext, m, err := decodeCBOR(rest)
		if err != nil || ext.Kind != cborMap {
			return nil, errors.New("webauthn: bad extensions")
		}
		rest = rest[m:]
	}
	if len(rest) != 0 {
		return nil, errors.New("webauthn: trailing bytes after authenticator data")
	}
	pub, alg, err := coseKey(key)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, err
	}
	c.Alg = alg
	c.PublicKey = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	return c, nil
}

// COSE key parameters (RFC 9053).
const (
	coseKty     = 1
	coseAlg     = 3
	coseKtyEC2  = 2
	coseKtyRSA  = 3
	coseCrvP256 = 1
)

func coseKey(k cborItem) (any, int, error) {
	if k.Kind != cborMap || !k.uniqueKeys() {
		return nil, 0, errors.New("webauthn: credential public key is not a COSE map")
	}
	intOf := func(label int64) (int64, bool) {
		v, ok := k.intKey(label)
		if !ok || (v.Kind != cborUint && v.Kind != cborNeg) {
			return 0, false
		}
		return v.Int, true
	}
	bytesOf := func(label int64) ([]byte, bool) {
		v, ok := k.intKey(label)
		if !ok || v.Kind != cborBytes {
			return nil, false
		}
		return v.Bytes, true
	}
	kty, ok1 := intOf(coseKty)
	alg, ok2 := intOf(coseAlg)
	if !ok1 || !ok2 {
		return nil, 0, errors.New("webauthn: COSE key without kty or alg")
	}
	switch {
	case kty == coseKtyEC2 && alg == AlgES256:
		crv, ok := intOf(-1)
		x, okx := bytesOf(-2)
		y, oky := bytesOf(-3)
		if !ok || crv != coseCrvP256 || !okx || !oky || len(x) != 32 || len(y) != 32 {
			return nil, 0, errors.New("webauthn: ES256 key must be P-256 with 32-byte x and y")
		}
		pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
		if err != nil {
			return nil, 0, errors.New("webauthn: ES256 key is not a point on P-256")
		}
		return pub, AlgES256, nil
	case kty == coseKtyRSA && alg == AlgRS256:
		n, okn := bytesOf(-1)
		e, oke := bytesOf(-2)
		if !okn || !oke || len(n) == 0 || n[0] == 0 || len(n) > 2048 || len(e) == 0 || len(e) > 4 {
			return nil, 0, errors.New("webauthn: RS256 key must have n and e")
		}
		var ev uint64
		for _, b := range e {
			ev = ev<<8 | uint64(b)
		}
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(ev)}
		if err := checkRSA(pub); err != nil {
			return nil, 0, err
		}
		return pub, AlgRS256, nil
	}
	return nil, 0, fmt.Errorf("webauthn: unsupported key (kty %d, alg %d): only ES256 and RS256", kty, alg)
}
