package sign

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// ServerIntent is what a server's owner authorises with a passkey registered at that server:
// Action on Server (its rp_id), with Subject the sha256 of the action's parameters (Subject)
// and Credential the passkey's id, base64url without padding.
type ServerIntent struct {
	Action     string `json:"action"`
	Server     string `json:"server"`
	Subject    string `json:"subject"`
	Nonce      string `json:"nonce"`
	Time       string `json:"time"`
	Credential string `json:"credential"`
}

const serverHeader = "truehand-server-intent/1"

var (
	actionRe     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	credentialRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// Message is the exact byte string whose sha256 is the WebAuthn challenge, in the form of
// Intent.Message under its own header.
func (i ServerIntent) Message() []byte {
	var b bytes.Buffer
	b.WriteString(serverHeader + "\n")
	for _, kv := range [][2]string{
		{"action", i.Action}, {"server", i.Server}, {"subject", i.Subject},
		{"nonce", i.Nonce}, {"time", i.Time}, {"credential", i.Credential},
	} {
		b.WriteString(kv[0] + ": " + kv[1] + "\n")
	}
	return b.Bytes()
}

// Challenge is the WebAuthn challenge that signs this intent.
func (i ServerIntent) Challenge() []byte {
	h := sha256.Sum256(i.Message())
	return h[:]
}

// Check rejects an intent whose fields could not have come from a well-formed request.
func (i ServerIntent) Check() error {
	if !actionRe.MatchString(i.Action) {
		return fmt.Errorf("server intent: invalid action %q", i.Action)
	}
	if err := CheckRPID(i.Server); err != nil {
		return fmt.Errorf("server intent: %w", err)
	}
	if !sha256Re.MatchString(i.Subject) {
		return errors.New("server intent: subject is not a lowercase sha256")
	}
	if !nonceRe.MatchString(i.Nonce) {
		return errors.New("server intent: nonce must be 32 to 64 lowercase hex characters")
	}
	if len(i.Credential) > 1364 || !credentialRe.MatchString(i.Credential) {
		return errors.New("server intent: credential must be a base64url id without padding")
	}
	if _, err := parseTime(i.Time); err != nil {
		return err
	}
	return nil
}

// Fresh reports whether the intent was signed within maxAge of now.
func (i ServerIntent) Fresh(now time.Time, maxAge time.Duration) error {
	return fresh(i.Time, now, maxAge)
}

// Subject is the sha256, in hex, of params as JSON with sorted keys: what a server intent
// binds an action's parameters with.
func Subject(params map[string]string) string {
	if params == nil {
		params = map[string]string{}
	}
	b, _ := json.Marshal(params)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// SignedServer is a server intent with its WebAuthn assertion, encoded as in Signed.
type SignedServer struct {
	Intent            ServerIntent `json:"intent"`
	Format            string       `json:"format"`
	Sig               string       `json:"sig"`
	AuthenticatorData string       `json:"authenticator_data"`
	ClientDataJSON    string       `json:"client_data_json"`
}

// Verify checks the intent's form and its WebAuthn signature by pub, the key of the passkey
// the intent names, registered for the server's rp_id.
func (s SignedServer) Verify(pub crypto.PublicKey, rpID string) error {
	return s.verify(pub, rpID, "")
}

func (s SignedServer) verify(pub crypto.PublicKey, rpID, origin string) error {
	if err := s.Intent.Check(); err != nil {
		return err
	}
	if s.Intent.Server != rpID {
		return fmt.Errorf("server intent: signed for server %q, this is %q", s.Intent.Server, rpID)
	}
	if s.Format != FormatWebAuthn {
		return errors.New("server intent: only a passkey signs for the owner")
	}
	return verifyMessage(s.Intent.Message(), s.Format, s.Sig, s.AuthenticatorData, s.ClientDataJSON, pub, rpID, origin)
}

// VerifyES256 checks the intent's form and a raw ES256 signature of its Message by pub, a
// Secure Enclave key (the Mac app anchoring the first owner), for the server rpID.
func (s SignedServer) VerifyES256(pub *ecdsa.PublicKey, rpID string) error {
	if err := s.Intent.Check(); err != nil {
		return err
	}
	if s.Intent.Server != rpID {
		return fmt.Errorf("server intent: signed for server %q, this is %q", s.Intent.Server, rpID)
	}
	if s.Format != FormatES256 {
		return errors.New("server intent: not an es256 signature")
	}
	return verifyMessage(s.Intent.Message(), s.Format, s.Sig, s.AuthenticatorData, s.ClientDataJSON, pub, rpID, "")
}

// SignCount is the authenticator's signature counter in the assertion (0 for es256).
func (s SignedServer) SignCount() (uint32, error) { return signCount(s.Format, s.AuthenticatorData) }

// SignCount is the authenticator's signature counter in the assertion (0 for es256).
func (s Signed) SignCount() (uint32, error) { return signCount(s.Format, s.AuthenticatorData) }

func signCount(format, authData string) (uint32, error) {
	if format != FormatWebAuthn {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(authData)
	if err != nil || len(b) < 37 {
		return 0, errors.New("webauthn: bad authenticator data")
	}
	return binary.BigEndian.Uint32(b[33:37]), nil
}

// CredentialID encodes a credential id as server intents name it.
func CredentialID(id []byte) string { return base64.RawURLEncoding.EncodeToString(id) }
