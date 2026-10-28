// Package sign verifies the human approvals a hardware key signs: an intent (approve this
// plan, push this commit) bound to the repo, the task and a single-use nonce, signed with
// ECDSA P-256 (ES256) by a key the base branch's config lists as an approver.
package sign

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Actions an intent can authorise.
const (
	ActionApprovePlan = "approve-plan"
	ActionPush        = "push"
	ActionOpenPR      = "open-pr"
)

// Signature formats. ES256 signs Message() directly; WebAuthn signs authenticatorData ||
// SHA-256(clientDataJSON), whose challenge is SHA-256(Message()).
const (
	FormatES256    = "es256"
	FormatWebAuthn = "webauthn"
)

// Intent is what a human authorises. Head is the commit for push and open-pr, empty for
// approve-plan. Time is RFC 3339 in UTC with seconds, as signed.
type Intent struct {
	Action     string `json:"action"`
	Repo       string `json:"repo"`
	Task       string `json:"task"`
	PlanSHA256 string `json:"plan_sha256"`
	Head       string `json:"head,omitempty"`
	Nonce      string `json:"nonce"`
	Time       string `json:"time"`
	Approver   string `json:"approver"`
}

const messageHeader = "truehand-intent/1"

var (
	sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)
	commitRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	nonceRe  = regexp.MustCompile(`^[0-9a-f]{32,64}$`)
	idRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// Message is the exact byte string that is signed: a header line, then one "key: value"
// line per field in this order, each ending in "\n".
func (i Intent) Message() []byte {
	var b bytes.Buffer
	b.WriteString(messageHeader + "\n")
	for _, kv := range [][2]string{
		{"action", i.Action}, {"repo", i.Repo}, {"task", i.Task}, {"plan_sha256", i.PlanSHA256},
		{"head", i.Head}, {"nonce", i.Nonce}, {"time", i.Time}, {"approver", i.Approver},
	} {
		b.WriteString(kv[0] + ": " + kv[1] + "\n")
	}
	return b.Bytes()
}

// Check rejects an intent whose fields could not have come from a well-formed request,
// including any field that could smuggle a line break into the message.
func (i Intent) Check() error {
	switch i.Action {
	case ActionApprovePlan:
		if i.Head != "" {
			return errors.New("intent: approve-plan has no head")
		}
	case ActionPush, ActionOpenPR:
		if !commitRe.MatchString(i.Head) {
			return fmt.Errorf("intent: head %q is not a commit id", i.Head)
		}
	default:
		return fmt.Errorf("intent: unknown action %q", i.Action)
	}
	if i.Repo == "" || strings.ContainsAny(i.Repo, "\r\n") {
		return errors.New("intent: repo must name the repo's origin, on one line")
	}
	if !idRe.MatchString(i.Task) {
		return fmt.Errorf("intent: invalid task %q", i.Task)
	}
	if !sha256Re.MatchString(i.PlanSHA256) {
		return fmt.Errorf("intent: plan_sha256 %q is not a lowercase sha256", i.PlanSHA256)
	}
	if !nonceRe.MatchString(i.Nonce) {
		return errors.New("intent: nonce must be 32 to 64 lowercase hex characters")
	}
	if !idRe.MatchString(i.Approver) {
		return fmt.Errorf("intent: invalid approver %q", i.Approver)
	}
	if _, err := i.When(); err != nil {
		return err
	}
	return nil
}

func (i Intent) When() (time.Time, error) {
	t, err := time.Parse(time.RFC3339, i.Time)
	if err != nil || t.Format("2006-01-02T15:04:05Z") != i.Time {
		return time.Time{}, fmt.Errorf("intent: time %q is not RFC 3339 UTC with seconds (2006-01-02T15:04:05Z)", i.Time)
	}
	return t, nil
}

// Fresh reports whether the intent was signed within maxAge of now (and not in the future
// beyond the same skew).
func (i Intent) Fresh(now time.Time, maxAge time.Duration) error {
	t, err := i.When()
	if err != nil {
		return err
	}
	if d := now.Sub(t); d > maxAge || d < -maxAge {
		return fmt.Errorf("intent signed at %s is not within %s of now", i.Time, maxAge)
	}
	return nil
}

// Signed is an intent with its signature. Sig is the ASN.1 DER ECDSA signature, standard
// base64. For WebAuthn, AuthenticatorData and ClientDataJSON are base64url without padding,
// as browsers return them.
type Signed struct {
	Intent            Intent `json:"intent"`
	Format            string `json:"format"`
	Sig               string `json:"sig"`
	AuthenticatorData string `json:"authenticator_data,omitempty"`
	ClientDataJSON    string `json:"client_data_json,omitempty"`
}

// Verify checks the intent's form and its signature by pub. rpID is the WebAuthn relying
// party the key was registered for; it is unused for ES256.
func (s Signed) Verify(pub *ecdsa.PublicKey, rpID string) error {
	if err := s.Intent.Check(); err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(s.Sig)
	if err != nil {
		return errors.New("signature is not standard base64")
	}
	switch s.Format {
	case FormatES256:
		if s.AuthenticatorData != "" || s.ClientDataJSON != "" {
			return errors.New("es256 signature carries WebAuthn fields")
		}
		h := sha256.Sum256(s.Intent.Message())
		if !ecdsa.VerifyASN1(pub, h[:], sig) {
			return errors.New("signature does not verify")
		}
		return nil
	case FormatWebAuthn:
		return verifyWebAuthn(pub, rpID, s, sig)
	}
	return fmt.Errorf("unknown signature format %q", s.Format)
}

// WebAuthn assertion flags: user present, user verified.
const (
	flagUP = 0x01
	flagUV = 0x04
)

func verifyWebAuthn(pub *ecdsa.PublicKey, rpID string, s Signed, sig []byte) error {
	if rpID == "" {
		return errors.New("webauthn: approver has no rp_id")
	}
	auth, err := base64.RawURLEncoding.DecodeString(s.AuthenticatorData)
	if err != nil || len(auth) < 37 {
		return errors.New("webauthn: bad authenticator data")
	}
	cdj, err := base64.RawURLEncoding.DecodeString(s.ClientDataJSON)
	if err != nil {
		return errors.New("webauthn: bad client data")
	}
	var cd struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(cdj, &cd); err != nil {
		return errors.New("webauthn: client data is not JSON")
	}
	want := sha256.Sum256(s.Intent.Message())
	if cd.Type != "webauthn.get" || cd.Challenge != base64.RawURLEncoding.EncodeToString(want[:]) {
		return errors.New("webauthn: client data does not name this intent")
	}
	rp := sha256.Sum256([]byte(rpID))
	if !bytes.Equal(auth[:32], rp[:]) {
		return errors.New("webauthn: signed for another relying party")
	}
	if auth[32]&flagUP == 0 || auth[32]&flagUV == 0 {
		return errors.New("webauthn: the user was not present and verified")
	}
	cdh := sha256.Sum256(cdj)
	h := sha256.Sum256(append(append([]byte{}, auth...), cdh[:]...))
	if !ecdsa.VerifyASN1(pub, h[:], sig) {
		return errors.New("webauthn: signature does not verify")
	}
	return nil
}

// ParsePublicKey reads a PEM "PUBLIC KEY" (SubjectPublicKeyInfo) holding a P-256 key.
func ParsePublicKey(s string) (*ecdsa.PublicKey, error) {
	block, rest := pem.Decode([]byte(strings.TrimSpace(s)))
	if block == nil || block.Type != "PUBLIC KEY" || len(bytes.TrimSpace(rest)) > 0 {
		return nil, errors.New("public key must be one PEM block of type PUBLIC KEY")
	}
	k, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("public key: %w", err)
	}
	ec, ok := k.(*ecdsa.PublicKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, errors.New("public key must be ECDSA P-256")
	}
	return ec, nil
}

// NewNonce returns 16 random bytes in hex.
func NewNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

var scpRe = regexp.MustCompile(`^(?:[^@/]+@)?([^:/]+):(.+)$`)

// NormalizeRepoURL reduces a git remote URL to host/path, lowercase, without scheme, user,
// port or ".git", so the https and ssh forms of one repo compare equal.
func NormalizeRepoURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}
	var host, path string
	if pu, err := url.Parse(u); err == nil && pu.Scheme != "" && pu.Host != "" {
		host, path = pu.Hostname(), pu.Path
	} else if m := scpRe.FindStringSubmatch(u); m != nil {
		host, path = m[1], m[2]
	} else {
		return strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git"))
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	return strings.ToLower(host + "/" + path)
}
