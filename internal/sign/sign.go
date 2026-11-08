// Package sign verifies the human approvals a hardware key signs: an intent (approve this
// plan, push this commit) bound to the repo, the task and a single-use nonce, signed with
// ECDSA P-256 (ES256) or, through WebAuthn, RSA (RS256) by a key the base branch's config
// lists as an approver.
package sign

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
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
	"strconv"
	"strings"
	"time"
)

// Actions an intent can authorise.
const (
	ActionApprovePlan = "approve-plan"
	ActionPush        = "push"
	ActionOpenPR      = "open-pr"
	// ActionCommitConfig commits a config to the base: Task is ConfigTask, PlanSHA256 the
	// sha256 of the config, Head the base commit it goes on.
	ActionCommitConfig = "commit-config"
	// ActionPushBase pushes the base: Task is ConfigTask, PlanSHA256 the sha256 of the
	// config at Head, Head the commit pushed.
	ActionPushBase = "push-base"
)

// ConfigTask is the task an intent on the base's config names.
const ConfigTask = "config"

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
	case ActionPush, ActionOpenPR, ActionCommitConfig, ActionPushBase:
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
	if (i.Action == ActionCommitConfig || i.Action == ActionPushBase) != (i.Task == ConfigTask) {
		return fmt.Errorf("intent: %s and task %q do not go together", i.Action, i.Task)
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

func (i Intent) When() (time.Time, error) { return parseTime(i.Time) }

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil || t.Format("2006-01-02T15:04:05Z") != s {
		return time.Time{}, fmt.Errorf("intent: time %q is not RFC 3339 UTC with seconds (2006-01-02T15:04:05Z)", s)
	}
	return t, nil
}

// Fresh reports whether the intent was signed within maxAge of now (and not in the future
// beyond the same skew).
func (i Intent) Fresh(now time.Time, maxAge time.Duration) error { return fresh(i.Time, now, maxAge) }

func fresh(ts string, now time.Time, maxAge time.Duration) error {
	t, err := parseTime(ts)
	if err != nil {
		return err
	}
	if d := now.Sub(t); d > maxAge || d < -maxAge {
		return fmt.Errorf("intent signed at %s is not within %s of now", ts, maxAge)
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

// Verify checks the intent's form and its signature by pub, an ECDSA P-256 or RSA key.
// rpID is the WebAuthn relying party (a host name) the key was registered for; it is unused
// for ES256.
func (s Signed) Verify(pub crypto.PublicKey, rpID string) error {
	if err := s.Intent.Check(); err != nil {
		return err
	}
	return verifyMessage(s.Intent.Message(), s.Format, s.Sig, s.AuthenticatorData, s.ClientDataJSON, pub, rpID, "")
}

// verifyMessage checks sig over msg. origin overrides the origin rpID implies (tests only).
func verifyMessage(msg []byte, format, sig64, authData, clientData string, pub crypto.PublicKey, rpID, origin string) error {
	sig, err := base64.StdEncoding.DecodeString(sig64)
	if err != nil {
		return errors.New("signature is not standard base64")
	}
	switch format {
	case FormatES256:
		if authData != "" || clientData != "" {
			return errors.New("es256 signature carries WebAuthn fields")
		}
		ec, ok := pub.(*ecdsa.PublicKey)
		if !ok || ec.Curve != elliptic.P256() {
			return errors.New("es256 needs an ECDSA P-256 key")
		}
		h := sha256.Sum256(msg)
		if !ecdsa.VerifyASN1(ec, h[:], sig) {
			return errors.New("signature does not verify")
		}
		return nil
	case FormatWebAuthn:
		return verifyWebAuthn(pub, rpID, origin, msg, authData, clientData, sig)
	}
	return fmt.Errorf("unknown signature format %q", format)
}

// WebAuthn authenticator data flags.
const (
	flagUP = 0x01
	flagUV = 0x04
	flagBE = 0x08
	flagBS = 0x10
	flagAT = 0x40
	flagED = 0x80
)

// CheckRPID checks an rp_id: a WebAuthn relying party id is a lowercase host name, never
// with a scheme or a port.
func CheckRPID(rpID string) error {
	if !hostRe.MatchString(rpID) {
		return fmt.Errorf("rp_id %q must be a lowercase host name, without scheme or port", rpID)
	}
	if last := rpID[strings.LastIndexByte(rpID, '.')+1:]; strings.Trim(last, "0123456789") == "" {
		return fmt.Errorf("rp_id %q is an IP address: WebAuthn needs a host name", rpID)
	}
	return nil
}

// SplitRPID checks rpID and returns it with the origin it has on the default https port.
// Signatures are accepted from that host on any https port (see OriginMatches).
func SplitRPID(rpID string) (host, origin string, err error) {
	if err := CheckRPID(rpID); err != nil {
		return "", "", err
	}
	return rpID, "https://" + rpID, nil
}

// OriginMatches reports whether a WebAuthn client data origin is rpID over https, on the
// default port or an explicit one: the port is not bound, since rpIdHash already binds the
// domain and tailscale serve may listen on 443, 8443 or 10000.
func OriginMatches(origin, rpID string) bool {
	rest, ok := strings.CutPrefix(origin, "https://"+rpID)
	if !ok {
		return false
	}
	if rest == "" {
		return true
	}
	port, ok := strings.CutPrefix(rest, ":")
	if !ok {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535 && strconv.Itoa(n) == port
}

var hostRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)

type clientData struct {
	Type        string `json:"type"`
	Challenge   string `json:"challenge"`
	Origin      string `json:"origin"`
	CrossOrigin *bool  `json:"crossOrigin"`
}

// checkClientData parses clientDataJSON and checks its type, challenge, origin (rpID on any
// https port, or exactly exact when set) and that it was not made in a cross-origin frame.
func checkClientData(cdj []byte, typ string, challenge []byte, rpID, exact string) error {
	var cd clientData
	if err := json.Unmarshal(cdj, &cd); err != nil {
		return errors.New("webauthn: client data is not JSON")
	}
	if cd.Type != typ {
		return fmt.Errorf("webauthn: client data type %q, want %s", cd.Type, typ)
	}
	if cd.Challenge != base64.RawURLEncoding.EncodeToString(challenge) {
		return errors.New("webauthn: client data does not name this intent")
	}
	if exact != "" && cd.Origin != exact || exact == "" && !OriginMatches(cd.Origin, rpID) {
		return fmt.Errorf("webauthn: signed on %q, not https://%s", cd.Origin, rpID)
	}
	if cd.CrossOrigin != nil && *cd.CrossOrigin {
		return errors.New("webauthn: signed in a cross-origin frame")
	}
	return nil
}

func verifyWebAuthn(pub crypto.PublicKey, rpID, origin string, msg []byte, authData, clientDataB64 string, sig []byte) error {
	if rpID == "" {
		return errors.New("webauthn: approver has no rp_id")
	}
	if err := CheckRPID(rpID); err != nil {
		return err
	}
	auth, err := base64.RawURLEncoding.DecodeString(authData)
	if err != nil || len(auth) < 37 {
		return errors.New("webauthn: bad authenticator data")
	}
	cdj, err := base64.RawURLEncoding.DecodeString(clientDataB64)
	if err != nil {
		return errors.New("webauthn: bad client data")
	}
	want := sha256.Sum256(msg)
	if err := checkClientData(cdj, "webauthn.get", want[:], rpID, origin); err != nil {
		return err
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
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		if k.Curve != elliptic.P256() || !ecdsa.VerifyASN1(k, h[:], sig) {
			return errors.New("webauthn: signature does not verify")
		}
	case *rsa.PublicKey:
		if checkRSA(k) != nil || rsa.VerifyPKCS1v15(k, crypto.SHA256, h[:], sig) != nil {
			return errors.New("webauthn: signature does not verify")
		}
	default:
		return errors.New("webauthn: unsupported key type")
	}
	return nil
}

func checkRSA(k *rsa.PublicKey) error {
	if k.N.BitLen() < 2048 || k.E != 65537 {
		return errors.New("public key: RSA must be at least 2048 bits with exponent 65537")
	}
	return nil
}

// ParsePublicKey reads a PEM "PUBLIC KEY" (SubjectPublicKeyInfo) holding an ECDSA P-256 key
// or an RSA key of at least 2048 bits with exponent 65537.
func ParsePublicKey(s string) (crypto.PublicKey, error) {
	block, rest := pem.Decode([]byte(strings.TrimSpace(s)))
	if block == nil || block.Type != "PUBLIC KEY" || len(bytes.TrimSpace(rest)) > 0 {
		return nil, errors.New("public key must be one PEM block of type PUBLIC KEY")
	}
	k, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("public key: %w", err)
	}
	switch k := k.(type) {
	case *ecdsa.PublicKey:
		if k.Curve == elliptic.P256() {
			return k, nil
		}
	case *rsa.PublicKey:
		if err := checkRSA(k); err != nil {
			return nil, err
		}
		return k, nil
	}
	return nil, errors.New("public key must be ECDSA P-256 or RSA")
}

// ParseP256PublicKey is ParsePublicKey restricted to ECDSA P-256 (a Secure Enclave key).
func ParseP256PublicKey(s string) (*ecdsa.PublicKey, error) {
	k, err := ParsePublicKey(s)
	if err != nil {
		return nil, err
	}
	ec, ok := k.(*ecdsa.PublicKey)
	if !ok {
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
