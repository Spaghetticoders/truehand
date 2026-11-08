package appstate

import (
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spaghetticoders/truehand/internal/sign"
)

const passkeysFile = "passkeys.json"

// Server-intent actions an owner (or the Mac) signs.
const (
	ActionOwner         = "owner"
	ActionAddOwner      = "add-owner"
	ActionRevokePasskey = "revoke-passkey"
	ActionAnchorOwner   = "anchor-owner"
	ActionEnrollCode    = "enroll-code"
)

// Grant kinds: how a passkey became an owner of this server.
const (
	GrantAnchor = "anchor" // named by the root-owned anchor file (Linux bootstrap)
	GrantMac    = "mac"    // signed by the Mac app's Secure Enclave key
	GrantOwner  = "owner"  // signed by another owner's passkey
)

// Passkey is a WebAuthn credential registered at this server. Owner asks for it to act as
// the server's owner, which holds only while its Grant leads back to an anchor: the file is
// the user's, so anyone running as the user can delete entries, never add a valid owner.
type Passkey struct {
	ID             string    `json:"id"` // credential id, base64url without padding
	PublicKey      string    `json:"public_key"`
	Alg            int       `json:"alg"`
	RPID           string    `json:"rp_id"`
	AAGUID         string    `json:"aaguid,omitempty"`
	UserVerified   bool      `json:"user_verified"`
	BackupEligible bool      `json:"backup_eligible"`
	BackupState    bool      `json:"backup_state"`
	SignCount      uint32    `json:"sign_count"`
	Device         string    `json:"device,omitempty"`
	Label          string    `json:"label"`
	Owner          bool      `json:"owner"`
	Created        time.Time `json:"created"`
	LastUsed       time.Time `json:"last_used,omitempty"`
	Grant          *Grant    `json:"grant,omitempty"`
}

// Grant is the proof a passkey is an owner. Params are what Signed's subject hashes.
type Grant struct {
	Kind      string             `json:"kind"`
	By        string             `json:"by,omitempty"`
	Params    map[string]string  `json:"params,omitempty"`
	Signed    *sign.SignedServer `json:"signed,omitempty"`
	CodeProof string             `json:"code_proof,omitempty"`
}

type Passkeys struct {
	Passkeys []Passkey `json:"passkeys"`
	// Soft are revocations made from a passkey: in the user's own file, so anyone running as
	// the user can undo them. `sudo truehand passkey revoke` makes one durable.
	Soft []Revocation `json:"soft_revoked,omitempty"`
}

func initPasskeys(p *Passkeys) {
	if p.Passkeys == nil {
		p.Passkeys = []Passkey{}
	}
}

func (s *Store) Passkeys() (*Passkeys, error) { return load(s, passkeysFile, initPasskeys) }

func (s *Store) UpdatePasskeys(fn func(*Passkeys) error) error {
	return update(s, passkeysFile, initPasskeys, fn)
}

func (ps *Passkeys) Get(id string) (*Passkey, bool) {
	for i := range ps.Passkeys {
		if ps.Passkeys[i].ID == id {
			return &ps.Passkeys[i], true
		}
	}
	return nil, false
}

// Remove drops a passkey; owners it granted keep only grants that lead elsewhere.
func (ps *Passkeys) Remove(id string) bool {
	for i := range ps.Passkeys {
		if ps.Passkeys[i].ID == id {
			ps.Passkeys = append(ps.Passkeys[:i], ps.Passkeys[i+1:]...)
			return true
		}
	}
	return false
}

// PublicKeySHA256 is the sha256, in hex, of a PEM public key as stored.
func PublicKeySHA256(pem string) string {
	h := sha256.Sum256([]byte(strings.TrimSpace(pem)))
	return hex.EncodeToString(h[:])
}

// KeyFingerprint is what people compare: the first 12 hex of the sha256 of the key's
// SubjectPublicKeyInfo (DER), which a browser computes from getPublicKey(); "invalid" for a
// key that does not parse.
func KeyFingerprint(pemKey string) string {
	pub, err := sign.ParsePublicKey(pemKey)
	if err != nil {
		return "invalid"
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "invalid"
	}
	h := sha256.Sum256(der)
	return hex.EncodeToString(h[:])[:12]
}

// Binding names a passkey in what owners sign about it: its id, key and relying party.
func (p *Passkey) Binding() map[string]string {
	return map[string]string{"credential": p.ID, "public_key_sha256": PublicKeySHA256(p.PublicKey), "rp_id": p.RPID}
}

func (p *Passkey) keepEntry() string { return p.ID + ":" + PublicKeySHA256(p.PublicKey) }

// KeepList is what a revocation's signature keeps as owners: id:key-sha256, sorted.
func KeepList(keep []*Passkey) string {
	var out []string
	for _, p := range keep {
		out = append(out, p.keepEntry())
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// Trust is what owner grants are checked against. Anchor is the root-owned anchor file's
// content (nil when there is none), MacKey the Mac app's key (nil off the Mac).
type Trust struct {
	Anchor *Anchor
	MacKey *ecdsa.PublicKey
	// Revoked are the durable revocations (root's list, the Mac's Keychain); Broken is set
	// when they could not be read, and then nobody is an owner.
	Revoked []Revocation
	Broken  error
}

// Durable reports whether p is revoked where the user cannot undo it.
func (t Trust) Durable(p *Passkey) bool { return revokedIn(t.Revoked, p) }

// Owners returns the ids of the passkeys whose grant leads back to an anchor.
// A revoked passkey is never one, whatever grant it carries: the durable revocations
// hold against anything written in the file; the file's own (Soft) only until someone
// rewrites it.
func (ps *Passkeys) Owners(t Trust) map[string]bool {
	valid := map[string]bool{}
	if t.Broken != nil {
		return valid
	}
	for changed := true; changed; {
		changed = false
		for i := range ps.Passkeys {
			p := &ps.Passkeys[i]
			if valid[p.ID] || !p.Owner || p.Grant == nil || t.Durable(p) || revokedIn(ps.Soft, p) {
				continue
			}
			if ps.grantOK(p, t, valid) == nil {
				valid[p.ID] = true
				changed = true
			}
		}
	}
	return valid
}

// GrantError says why a passkey that asks to be an owner is not one (nil if it is).
func (ps *Passkeys) GrantError(id string, t Trust) error {
	p, ok := ps.Get(id)
	if !ok {
		return ErrNotFound
	}
	if !p.Owner {
		return errors.New("not an owner")
	}
	if t.Broken != nil {
		return fmt.Errorf("the revocation list cannot be read: %w", t.Broken)
	}
	if t.Durable(p) || revokedIn(ps.Soft, p) {
		return errors.New("revoked")
	}
	if p.Grant == nil {
		return errors.New("waiting for confirmation")
	}
	return ps.grantOK(p, t, ps.Owners(t))
}

func (ps *Passkeys) grantOK(p *Passkey, t Trust, valid map[string]bool) error {
	g := p.Grant
	switch g.Kind {
	case GrantAnchor:
		a := t.Anchor
		if a == nil || a.Owner == nil {
			return errors.New("no anchor names an owner")
		}
		if a.Owner.Credential != p.ID || a.Owner.PublicKeySHA256 != PublicKeySHA256(p.PublicKey) || a.Owner.RPID != p.RPID {
			return errors.New("the anchor names another passkey")
		}
		return nil
	case GrantMac:
		if t.MacKey == nil {
			return errors.New("the Mac app's key is not known to this server")
		}
		if g.Signed == nil {
			return errors.New("mac grant: no signature")
		}
		in := g.Signed.Intent
		if in.Action != ActionAnchorOwner || in.Server != p.RPID || in.Credential != p.ID || in.Subject != sign.Subject(p.Binding()) {
			return errors.New("mac grant: signed for something else")
		}
		if err := g.Signed.VerifyES256(t.MacKey, p.RPID); err != nil {
			return fmt.Errorf("mac grant: %w", err)
		}
		return nil
	case GrantOwner:
		if g.By == p.ID || !valid[g.By] {
			return errors.New("granted by a passkey that is not an owner")
		}
		by, ok := ps.Get(g.By)
		if !ok || g.Signed == nil {
			return errors.New("granted by an unknown passkey")
		}
		in := g.Signed.Intent
		if in.Credential != by.ID || in.Server != by.RPID || in.Subject != sign.Subject(g.Params) {
			return errors.New("owner grant: signed for something else")
		}
		switch in.Action {
		case ActionAddOwner:
			if !sameParams(g.Params, p.Binding()) {
				return errors.New("owner grant: names another passkey")
			}
		case ActionRevokePasskey:
			if !contains(strings.Split(g.Params["keep"], ","), p.keepEntry()) {
				return errors.New("owner grant: revocation does not keep this passkey")
			}
		default:
			return fmt.Errorf("owner grant: action %q grants nothing", in.Action)
		}
		pub, err := sign.ParsePublicKey(by.PublicKey)
		if err != nil {
			return err
		}
		return g.Signed.Verify(pub, by.RPID)
	}
	return fmt.Errorf("unknown grant kind %q", g.Kind)
}

func sameParams(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// Anchor is the root-owned file that makes the first owner of a server without a Mac:
// `sudo truehand passkey bootstrap` writes the hash of a one-time code, then the passkey
// registered with that code.
type Anchor struct {
	Version    int          `json:"version"`
	UID        int          `json:"uid"`
	CodeSHA256 string       `json:"code_sha256,omitempty"`
	Expires    time.Time    `json:"expires,omitempty"`
	Owner      *AnchorOwner `json:"owner,omitempty"`
}

type AnchorOwner struct {
	Credential      string    `json:"credential"`
	PublicKeySHA256 string    `json:"public_key_sha256"`
	RPID            string    `json:"rp_id"`
	Label           string    `json:"label,omitempty"`
	Created         time.Time `json:"created"`
}

// AnchorPath is where the anchor lives; tests point it elsewhere.
var AnchorPath = "/etc/truehand/anchor.json"

// AnchorFileOK checks that the anchor and its directory can be changed only by root; tests
// replace it, since they cannot make root's files.
var AnchorFileOK = rootOwned

func rootOwned(fi os.FileInfo) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("cannot read the owner")
	}
	if st.Uid != 0 {
		return fmt.Errorf("%s is not owned by root", fi.Name())
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by others than root", fi.Name())
	}
	return nil
}

// readRootJSON reads a file in the anchor's directory that only root may change: false
// when it does not exist.
func readRootJSON(path string, v any) (bool, error) {
	dir := filepath.Dir(path)
	di, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !di.IsDir() {
		return false, fmt.Errorf("%s is not a directory", dir)
	}
	if err := AnchorFileOK(di); err != nil {
		return false, err
	}
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() {
		return false, fmt.Errorf("%s is not a regular file", path)
	}
	if err := AnchorFileOK(fi); err != nil {
		return false, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return true, nil
}

// ReadAnchor reads the anchor for uid: nil, nil when there is none.
func ReadAnchor(uid int) (*Anchor, error) {
	var a Anchor
	ok, err := readRootJSON(AnchorPath, &a)
	if err != nil || !ok {
		return nil, err
	}
	if a.Version != 1 {
		return nil, fmt.Errorf("%s: unknown version %d", AnchorPath, a.Version)
	}
	if a.UID != uid {
		return nil, nil
	}
	return &a, nil
}

// Revocation names a passkey that is never an owner again, by id and by key, so neither a
// new id nor a new label brings it back.
type Revocation struct {
	Credential      string `json:"credential"`
	PublicKeySHA256 string `json:"public_key_sha256,omitempty"`
	// Key is the fingerprint people compare (KeyFingerprint); only shown, never matched.
	Key   string    `json:"key,omitempty"`
	Label string    `json:"label,omitempty"`
	Time  time.Time `json:"time"`
}

// RevokedList is the root-owned list `sudo truehand passkey revoke` writes next to the anchor.
type RevokedList struct {
	Version int          `json:"version"`
	UID     int          `json:"uid"`
	Revoked []Revocation `json:"revoked"`
}

// RevokedPath is the durable revocation list, beside the anchor.
func RevokedPath() string { return filepath.Join(filepath.Dir(AnchorPath), "revoked.json") }

// ReadRevoked reads the durable revocations for uid. An unreadable list is an error: the
// caller then trusts no owner rather than forget a revocation.
func ReadRevoked(uid int) ([]Revocation, error) {
	var l RevokedList
	ok, err := readRootJSON(RevokedPath(), &l)
	if err != nil || !ok {
		return nil, err
	}
	if l.Version != 1 {
		return nil, fmt.Errorf("%s: unknown version %d", RevokedPath(), l.Version)
	}
	if l.UID != uid {
		// Root's list is for one user: one for another user names none of this user's
		// revocations, so it cannot be trusted to hold them.
		return nil, fmt.Errorf("%s is the revocation list of uid %d, not of uid %d", RevokedPath(), l.UID, uid)
	}
	return l.Revoked, nil
}

// RevocationOf records p as revoked.
func RevocationOf(p *Passkey, now time.Time) Revocation {
	return Revocation{Credential: p.ID, PublicKeySHA256: PublicKeySHA256(p.PublicKey), Key: KeyFingerprint(p.PublicKey), Label: p.Label, Time: now.UTC().Truncate(time.Second)}
}

func revokedIn(list []Revocation, p *Passkey) bool {
	sha := PublicKeySHA256(p.PublicKey)
	for _, r := range list {
		if r.Credential == p.ID || r.PublicKeySHA256 != "" && r.PublicKeySHA256 == sha {
			return true
		}
	}
	return false
}

// CodeMatches reports whether code is the anchor's pending one-time code, still valid.
func (a *Anchor) CodeMatches(code string, now time.Time) bool {
	return a != nil && a.CodeSHA256 != "" && now.Before(a.Expires) && MatchSecret(a.CodeSHA256, code)
}

// CodeProof binds a passkey to the one-time code it was registered with: only who knows the
// code (the server that received it, and root that made it) can compute it.
func CodeProof(code string, p *Passkey) string {
	m := hmac.New(sha256.New, []byte(code))
	m.Write([]byte("truehand-anchor/1\n" + p.ID + "\n" + PublicKeySHA256(p.PublicKey) + "\n" + p.RPID + "\n"))
	return hex.EncodeToString(m.Sum(nil))
}

// CodeProofOK checks a passkey's code proof in constant time.
func CodeProofOK(code string, p *Passkey) bool {
	return p.Grant != nil && p.Grant.CodeProof != "" && hmac.Equal([]byte(CodeProof(code, p)), []byte(p.Grant.CodeProof))
}
