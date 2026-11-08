package serve

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spaghetticoders/truehand/internal/appstate"
	"github.com/spaghetticoders/truehand/internal/sign"
)

// Passkeys: WebAuthn credentials registered at this server. An owner's passkey is the human
// factor wherever the Mac's Touch ID would be (human()), from any browser that reaches the
// server over HTTPS. Registering one needs a one-time enrolment code that only a human
// could have released; becoming an owner needs a grant that leads back to an anchor
// (appstate.Passkeys.Owners).

const (
	ownerHeader     = "X-Truehand-Owner"
	enrollTTL       = 10 * time.Minute
	ceremonyTTL     = 5 * time.Minute
	ownerNonceTTL   = 2 * time.Minute
	codeOwnerNeeded = "owner-required"
	// pendingTTL is how long a registration may wait to be confirmed by the Mac or an owner.
	pendingTTL = 24 * time.Hour
)

// registeredKey is a registration this process made: only those can be confirmed, since
// anyone running as the user can write entries (or swap keys) in the passkeys file.
type registeredKey struct {
	sha     string
	expires time.Time
}

// madeHere reports whether p, as it is in the file, is a registration this process made.
func (s *Server) madeHere(p *appstate.Passkey) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.registered[p.ID]
	return ok && time.Now().Before(k.expires) && k.sha == appstate.PublicKeySHA256(p.PublicKey)
}

type enrollCode struct {
	expires time.Time
	owner   bool
	by      string // "mac", or the id of the owner passkey that released it
}

type ceremony struct {
	expires   time.Time
	challenge []byte
	rpID      string
	codeHash  string
	code      string // kept for the anchor's code proof; memory only
	anchor    bool
	owner     bool
	label     string
}

type ownerNonce struct {
	expires    time.Time
	action     string
	subject    string
	credential string
}

// registration is a verified navigator.credentials.create answer.
type registration struct {
	ID        []byte
	PublicKey string
	Alg       int
	SignCount uint32
	AAGUID    [16]byte
	UV        bool
	BE        bool
	BS        bool
}

// verifyRegistration is sign's check of a WebAuthn registration; tests may replace it.
var verifyRegistration = func(attestationObject, clientDataJSON string, challenge []byte, rpID string) (registration, error) {
	att, err1 := base64.RawURLEncoding.DecodeString(attestationObject)
	cdj, err2 := base64.RawURLEncoding.DecodeString(clientDataJSON)
	if err1 != nil || err2 != nil {
		return registration{}, errors.New("attestationObject and clientDataJSON must be base64url without padding")
	}
	c, err := sign.VerifyRegistration(rpID, challenge, cdj, att)
	if err != nil {
		return registration{}, err
	}
	// VerifyRegistration refuses a user that was not verified.
	return registration{ID: c.ID, PublicKey: c.PublicKey, Alg: c.Alg, SignCount: c.SignCount, AAGUID: c.AAGUID, UV: true, BE: c.BackupEligible, BS: c.BackedUp}, nil
}

// rpIDOf is the WebAuthn relying party a request reaches: its Host, when it came over HTTPS.
func rpIDOf(r *http.Request) (string, error) {
	if !secureRequest(r) {
		return "", errors.New("passkeys need HTTPS: open Truehand through tailscale serve (https://….ts.net)")
	}
	// The relying party is the host alone; the port is only in the origin.
	host := strings.ToLower(r.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if err := sign.CheckRPID(host); err != nil {
		return "", err
	}
	return host, nil
}

func (s *Server) anchor() *appstate.Anchor {
	if s.ReadAnchor == nil {
		a, _ := appstate.ReadAnchor(os.Getuid())
		return a
	}
	a, _ := s.ReadAnchor()
	return a
}

func (s *Server) trust() appstate.Trust {
	s.mu.Lock()
	mk := s.MacKey
	revoked := append([]appstate.Revocation{}, s.macRevoked...)
	s.mu.Unlock()
	read := s.ReadRevoked
	if read == nil {
		read = func() ([]appstate.Revocation, error) { return appstate.ReadRevoked(os.Getuid()) }
	}
	root, err := read()
	s.mu.Lock()
	macSent := s.macRevoked != nil
	s.mu.Unlock()
	if err == nil && !s.Headless && !macSent {
		// With a Mac app, its Keychain holds the revocations: until it sent them to this
		// process, a server restarted without it must not bring a revoked passkey back.
		return appstate.Trust{Anchor: s.anchor(), MacKey: mk, Broken: errNoMacRevocations}
	}
	if err != nil {
		err = fmt.Errorf("%w: until it is fixed no passkey is an owner. If it is not root's, `sudo truehand passkey revoke --repair` rewrites it; if it is a link, check its target and copy the real list in its place; if it does not parse, `sudo truehand passkey revoke --repair` rewrites it keeping every id it can read (by hand: `sudo mv %s %s.bak`, then `sudo truehand passkey revoke <id>` for each id in the .bak)", err, appstate.RevokedPath(), appstate.RevokedPath())
	}
	return appstate.Trust{Anchor: s.anchor(), MacKey: mk, Revoked: append(revoked, root...), Broken: err}
}

// SetMacRevoked is the Mac app's Keychain revocations, as it passed them on stdin.
func (s *Server) SetMacRevoked(list []appstate.Revocation) {
	if list == nil {
		list = []appstate.Revocation{}
	}
	s.mu.Lock()
	s.macRevoked = list
	s.mu.Unlock()
}

var errNoMacRevocations = errors.New("waiting for the Truehand Mac app's revocations: open the app")

// shellRevoked takes the durable revocations the Mac app keeps in its Keychain: it sends
// the whole list when it starts and after each revocation.
func (s *Server) shellRevoked(w http.ResponseWriter, r *http.Request) {
	if id := identityOf(r); id.Via != viaToken || !id.Shell {
		httpError(w, http.StatusForbidden, "only the Truehand Mac app sends its revocations")
		return
	}
	var body struct {
		Revoked []appstate.Revocation `json:"revoked"`
	}
	if !decode(w, r, &body) {
		return
	}
	for _, x := range body.Revoked {
		if x.Credential == "" && x.PublicKeySHA256 == "" {
			httpError(w, http.StatusBadRequest, "each revocation names a credential or a key")
			return
		}
	}
	if body.Revoked == nil {
		body.Revoked = []appstate.Revocation{}
	}
	s.mu.Lock()
	s.macRevoked = body.Revoked
	s.mu.Unlock()
	s.broadcast(sseEvent{"passkeys", nil})
	w.WriteHeader(http.StatusNoContent)
}

// owners are the passkeys that act as this server's owner now.
func (s *Server) owners() (*appstate.Passkeys, map[string]bool, error) {
	ps, err := s.Store.Passkeys()
	if err != nil {
		return nil, nil, err
	}
	return ps, ps.Owners(s.trust()), nil
}

// HasOwner reports whether any passkey acts as this server's owner.
func (s *Server) HasOwner() bool {
	_, valid, err := s.owners()
	return err == nil && len(valid) > 0
}

func ownerRequired(w http.ResponseWriter) {
	writeJSONStatus(w, http.StatusConflict, map[string]string{
		"error": "this server has no screen: confirm with an owner's passkey (register one with `sudo truehand passkey bootstrap` on the server)",
		"code":  codeOwnerNeeded,
	})
}

const enrollAlphabet = pairAlphabet

func randomCode(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = enrollAlphabet[int(b[i])%len(enrollAlphabet)]
	}
	return string(b)
}

func (s *Server) sweep(now time.Time) {
	for h, c := range s.enrolls {
		if now.After(c.expires) {
			delete(s.enrolls, h)
		}
	}
	for id, c := range s.ceremonies {
		if now.After(c.expires) {
			delete(s.ceremonies, id)
		}
	}
	for n, o := range s.ownerNonces {
		if now.After(o.expires) {
			delete(s.ownerNonces, n)
		}
	}
}

// enrollCodeNew releases a one-time code to register a passkey, owner or not: a human act.
func (s *Server) enrollCodeNew(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Owner bool `json:"owner"`
	}
	if !decode(w, r, &body) {
		return
	}
	what := "a passkey"
	if body.Owner {
		what = "an owner passkey"
	}
	if !s.human(w, r, "Truehand: register "+what+" for this server", nil) {
		return
	}
	by := "mac"
	if p := identityOf(r).proof; p != nil && p.owner != "" {
		by = p.owner
	}
	code := randomCode(8)
	exp := time.Now().Add(enrollTTL)
	s.mu.Lock()
	s.sweep(time.Now())
	s.enrolls[appstate.HashSecret(code)] = enrollCode{expires: exp, owner: body.Owner, by: by}
	s.mu.Unlock()
	writeJSON(w, map[string]any{"code": code, "expires": exp.UTC(), "owner": body.Owner})
}

// Wrong enrolment codes are limited per source, so one noisy client cannot keep everyone's
// enrolment shut, and overall, so many sources cannot guess fast.
const enrollMaxFailsTotal = 200

// enrollSource is who tries a code: its device, else the client address tailscale serve
// forwards, else the connection's.
func enrollSource(r *http.Request) string {
	if id := identityOf(r); id.Device != nil {
		return "d:" + id.Device.ID
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if secureRequest(r) && net.ParseIP(host).IsLoopback() {
		// The last entry is the address tailscale saw; earlier ones are the client's to write.
		parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		if xff := strings.TrimSpace(parts[len(parts)-1]); xff != "" {
			return "f:" + xff
		}
	}
	return "a:" + host
}

func recent(ts []time.Time, now time.Time) []time.Time {
	out := ts[:0]
	for _, t := range ts {
		if now.Sub(t) < time.Minute {
			out = append(out, t)
		}
	}
	return out
}

// tooManyFails records a wrong code from source when failed, and reports whether that source
// or everyone together has hit the limit. Callers hold s.mu.
func (s *Server) tooManyFails(source string, now time.Time, failed bool) bool {
	s.enrollFailAll = recent(s.enrollFailAll, now)
	for k, v := range s.enrollFail {
		if v = recent(v, now); len(v) == 0 {
			delete(s.enrollFail, k)
		} else {
			s.enrollFail[k] = v
		}
	}
	if failed {
		s.enrollFail[source] = append(s.enrollFail[source], now)
		s.enrollFailAll = append(s.enrollFailAll, now)
	}
	return len(s.enrollFail[source]) >= pairMaxFails || len(s.enrollFailAll) >= enrollMaxFailsTotal
}

func b64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// userHandle is the WebAuthn user id: one per server user, nothing personal in it.
func userHandle() []byte {
	host, _ := os.Hostname()
	h := sha256.Sum256([]byte(fmt.Sprintf("truehand-user/1\n%s\n%d", host, os.Getuid())))
	return h[:16]
}

// registerOptions starts a registration with an enrolment code; it needs no other
// credential, since a new phone has none.
func (s *Server) registerOptions(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code  string `json:"code"`
		Label string `json:"label"`
	}
	if !decode(w, r, &body) {
		return
	}
	label := strings.TrimSpace(body.Label)
	if label == "" || len(label) > 60 {
		httpError(w, http.StatusBadRequest, "label must be 1-60 characters")
		return
	}
	rpID, err := rpIDOf(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	code := normalizeCode(body.Code)
	h := appstate.HashSecret(code)
	now := time.Now()
	c := ceremony{expires: now.Add(ceremonyTTL), challenge: make([]byte, 32), rpID: rpID, codeHash: h, code: code, label: label}
	_, _ = rand.Read(c.challenge)

	s.mu.Lock()
	s.sweep(now)
	src := enrollSource(r)
	if s.tooManyFails(src, now, false) {
		s.mu.Unlock()
		httpError(w, http.StatusTooManyRequests, "too many wrong codes: wait a minute and make a new one")
		return
	}
	e, ok := s.enrolls[h]
	anchorUsed := s.anchorCodes[h]
	s.mu.Unlock()
	switch {
	case ok:
		c.owner = e.owner
	case !anchorUsed && s.anchor().CodeMatches(code, now):
		c.owner, c.anchor = true, true
	default:
		s.mu.Lock()
		s.tooManyFails(src, now, true)
		s.mu.Unlock()
		httpError(w, http.StatusUnauthorized, "wrong or expired code")
		return
	}
	ps, err := s.Store.Passkeys()
	if err != nil {
		fail(w, err)
		return
	}
	exclude := []map[string]any{}
	for _, p := range ps.Passkeys {
		if p.RPID == rpID {
			exclude = append(exclude, map[string]any{"type": "public-key", "id": p.ID})
		}
	}
	id := appstate.NewID("c_")
	s.mu.Lock()
	s.ceremonies[id] = c
	s.mu.Unlock()
	host := rpID
	hostname, _ := os.Hostname()
	writeJSON(w, map[string]any{
		"ceremony": id,
		"owner":    c.owner,
		"publicKey": map[string]any{
			"challenge": b64u(c.challenge),
			"rp":        map[string]string{"id": host, "name": "Truehand"},
			"user":      map[string]string{"id": b64u(userHandle()), "name": "truehand@" + strings.TrimSuffix(hostname, ".local"), "displayName": "Truehand " + strings.TrimSuffix(hostname, ".local")},
			"pubKeyCredParams": []map[string]any{
				{"type": "public-key", "alg": -7},
				{"type": "public-key", "alg": -257},
			},
			"authenticatorSelection": map[string]any{"residentKey": "preferred", "userVerification": "required"},
			"attestation":            "none",
			"excludeCredentials":     exclude,
			"timeout":                int(ceremonyTTL / time.Millisecond),
		},
	})
}

// register finishes a registration. A browser without a device cookie is paired by it.
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ceremony          string   `json:"ceremony"`
		AttestationObject string   `json:"attestationObject"`
		ClientDataJSON    string   `json:"clientDataJSON"`
		Transports        []string `json:"transports"`
	}
	if !decode(w, r, &body) {
		return
	}
	now := time.Now()
	s.mu.Lock()
	s.sweep(now)
	c, ok := s.ceremonies[body.Ceremony]
	delete(s.ceremonies, body.Ceremony)
	_, live := s.enrolls[c.codeHash]
	if ok && c.anchor {
		live = !s.anchorCodes[c.codeHash]
	}
	s.mu.Unlock()
	if !ok || !live {
		httpError(w, http.StatusUnauthorized, "registration expired: start again")
		return
	}
	if rpID, err := rpIDOf(r); err != nil || rpID != c.rpID {
		httpError(w, http.StatusBadRequest, "registration started at another address")
		return
	}
	reg, err := verifyRegistration(body.AttestationObject, body.ClientDataJSON, c.challenge, c.rpID)
	if err != nil {
		httpError(w, http.StatusBadRequest, "passkey refused: "+err.Error())
		return
	}
	if !reg.UV {
		httpError(w, http.StatusBadRequest, "passkey refused: the authenticator did not verify the user")
		return
	}
	p := appstate.Passkey{
		ID: sign.CredentialID(reg.ID), PublicKey: reg.PublicKey, Alg: reg.Alg, RPID: c.rpID,
		AAGUID: hex.EncodeToString(reg.AAGUID[:]), UserVerified: reg.UV, BackupEligible: reg.BE, BackupState: reg.BS,
		SignCount: reg.SignCount, Label: c.label, Owner: c.owner, Created: now.UTC().Truncate(time.Second),
	}
	if c.anchor {
		p.Grant = &appstate.Grant{Kind: appstate.GrantAnchor, CodeProof: appstate.CodeProof(c.code, &p)}
	}
	// The code is spent before anything is stored: one code, one passkey.
	s.mu.Lock()
	if c.anchor {
		if s.anchorCodes[c.codeHash] {
			s.mu.Unlock()
			httpError(w, http.StatusUnauthorized, "registration expired: start again")
			return
		}
		s.anchorCodes[c.codeHash] = true
	} else {
		if _, ok := s.enrolls[c.codeHash]; !ok {
			s.mu.Unlock()
			httpError(w, http.StatusUnauthorized, "registration expired: start again")
			return
		}
		delete(s.enrolls, c.codeHash)
	}
	s.mu.Unlock()

	id := identityOf(r)
	if id.Device != nil {
		p.Device = id.Device.ID
	} else if id.Via == "" {
		d, err := s.addDevice(w, r, appstate.NewID("d_"), c.label)
		if err != nil {
			fail(w, err)
			return
		}
		p.Device = d.ID
	}
	err = s.Store.UpdatePasskeys(func(ps *appstate.Passkeys) error {
		if _, dup := ps.Get(p.ID); dup {
			return fmt.Errorf("passkey %s: %w", p.ID, appstate.ErrExists)
		}
		ps.Passkeys = append(ps.Passkeys, p)
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	s.mu.Lock()
	s.registered[p.ID] = registeredKey{sha: appstate.PublicKeySHA256(p.PublicKey), expires: now.Add(pendingTTL)}
	s.mu.Unlock()
	s.broadcast(sseEvent{"passkeys", nil})
	ps, valid, _ := s.owners()
	writeJSON(w, s.passkeyView(ps, valid, &p, identityOf(r)))
}

type passkeyView struct {
	ID         string    `json:"id"`
	Label      string    `json:"label"`
	RPID       string    `json:"rpId"`
	Alg        string    `json:"alg"`
	Owner      bool      `json:"owner"`
	OwnerValid bool      `json:"ownerValid"`
	Pending    string    `json:"pending,omitempty"` // mac | owner | anchor: who must confirm; expired: register again
	Problem    string    `json:"problem,omitempty"`
	Synced     bool      `json:"synced"`
	Syncable   bool      `json:"syncable"`
	AAGUID     string    `json:"aaguid,omitempty"`
	SignCount  uint32    `json:"signCount"`
	Device     string    `json:"device,omitempty"`
	ThisDevice bool      `json:"thisDevice"`
	Key        string    `json:"key"`
	PublicKey  string    `json:"publicKey"`
	Created    time.Time `json:"created"`
	LastUsed   time.Time `json:"lastUsed,omitzero"`
	// Revoked: "durable" (root's list or the Mac's Keychain) or "soft" (only in the file).
	Revoked string `json:"revoked,omitempty"`
}

type revocationView struct {
	ID string `json:"id"`
	// Key is the fingerprint shown everywhere (12 hex of sha256 of the SPKI DER); KeySHA256
	// the full hash of the stored key, which is what revocations match.
	Key       string    `json:"key,omitempty"`
	KeySHA256 string    `json:"keySha256,omitempty"`
	Label     string    `json:"label,omitempty"`
	Revoked   string    `json:"revoked"`
	Time      time.Time `json:"time"`
}

func algName(a int) string {
	switch a {
	case -7:
		return "ES256"
	case -257:
		return "RS256"
	}
	return fmt.Sprint(a)
}

func (s *Server) passkeyView(ps *appstate.Passkeys, valid map[string]bool, p *appstate.Passkey, id identity) passkeyView {
	v := passkeyView{
		ID: p.ID, Label: p.Label, RPID: p.RPID, Alg: algName(p.Alg), Owner: p.Owner, OwnerValid: valid[p.ID],
		Synced: p.BackupState, Syncable: p.BackupEligible, AAGUID: p.AAGUID, SignCount: p.SignCount, Device: p.Device,
		ThisDevice: id.Device != nil && id.Device.ID == p.Device, Key: keyFingerprint(p.PublicKey), PublicKey: p.PublicKey,
		Created: p.Created, LastUsed: p.LastUsed,
	}
	t := s.trust()
	switch {
	case t.Durable(p):
		v.Revoked = "durable"
	case softRevoked(ps, p):
		v.Revoked = "soft"
	}
	if p.Owner && !v.OwnerValid && v.Revoked == "" {
		switch {
		case p.Grant == nil && !s.madeHere(p):
			// Registered before this server started (or written in the file by someone):
			// it cannot be confirmed; the device registers again.
			v.Pending = "expired"
		case p.Grant == nil && !s.Headless:
			v.Pending = "mac"
		case p.Grant == nil:
			v.Pending = "owner"
		case p.Grant.Kind == appstate.GrantAnchor && (s.anchor() == nil || s.anchor().Owner == nil):
			v.Pending = "anchor"
		default:
			if err := ps.GrantError(p.ID, s.trust()); err != nil {
				v.Problem = err.Error()
			}
		}
	}
	return v
}

func (s *Server) passkeys(w http.ResponseWriter, r *http.Request) {
	ps, valid, err := s.owners()
	if err != nil {
		fail(w, err)
		return
	}
	id := identityOf(r)
	out := []passkeyView{}
	for i := range ps.Passkeys {
		out = append(out, s.passkeyView(ps, valid, &ps.Passkeys[i], id))
	}
	t := s.trust()
	revs := []revocationView{}
	seen := map[string]bool{}
	fingerprint := func(x appstate.Revocation) string {
		if x.Key != "" {
			return x.Key
		}
		if p, ok := ps.Get(x.Credential); ok {
			return keyFingerprint(p.PublicKey)
		}
		return ""
	}
	for _, x := range t.Revoked {
		seen[x.Credential] = true
		revs = append(revs, revocationView{ID: x.Credential, Key: fingerprint(x), KeySHA256: x.PublicKeySHA256, Label: x.Label, Revoked: "durable", Time: x.Time})
	}
	for _, x := range ps.Soft {
		if !seen[x.Credential] {
			revs = append(revs, revocationView{ID: x.Credential, Key: fingerprint(x), KeySHA256: x.PublicKeySHA256, Label: x.Label, Revoked: "soft", Time: x.Time})
		}
	}
	rpID, rpErr := rpIDOf(r)
	res := map[string]any{"passkeys": out, "revocations": revs, "headless": s.Headless, "hasOwner": len(valid) > 0, "rpId": rpID}
	if t.Broken != nil {
		res["revocationsError"] = t.Broken.Error()
		// The UI words it from the code: mac-revocations-missing or revoked-list-invalid.
		res["revocationsCode"] = "revoked-list-invalid"
		if errors.Is(t.Broken, errNoMacRevocations) {
			res["revocationsCode"] = "mac-revocations-missing"
		}
	}
	if rpErr != nil {
		res["rpIdError"] = rpErr.Error()
	}
	writeJSON(w, res)
}

// ownerChallenge prepares the server intent an owner's passkey signs: for a human action
// (method, path and the hash of the body it will send), or to grant or revoke a passkey.
func (s *Server) ownerChallenge(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Credential string `json:"credential"`
		Action     string `json:"action"`
		Method     string `json:"method"`
		Path       string `json:"path"`
		BodySHA256 string `json:"bodySha256"`
		Passkey    string `json:"passkey"`
	}
	if !decode(w, r, &body) {
		return
	}
	rpID, err := rpIDOf(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	ps, valid, err := s.owners()
	if err != nil {
		fail(w, err)
		return
	}
	by, ok := ps.Get(body.Credential)
	if !ok || !valid[by.ID] {
		httpError(w, http.StatusForbidden, "that passkey is not an owner of this server")
		return
	}
	if by.RPID != rpID {
		httpError(w, http.StatusBadRequest, fmt.Sprintf("that passkey was registered at %s: open Truehand there", by.RPID))
		return
	}
	var subject, key string
	switch body.Action {
	case appstate.ActionOwner:
		if !strings.HasPrefix(body.Path, "/api/") || !isUnsafe(body.Method) || !sha256Hex(body.BodySHA256) {
			httpError(w, http.StatusBadRequest, "method, path (with its query, if any) and bodySha256 name the request to confirm")
			return
		}
		subject = humanSubject(body.Method, body.Path, body.BodySHA256)
	case appstate.ActionAddOwner:
		p, ok := ps.Get(body.Passkey)
		if !ok || !p.Owner || valid[p.ID] || !s.madeHere(p) {
			httpError(w, http.StatusBadRequest, "passkey must be an owner passkey registered here and waiting for confirmation")
			return
		}
		subject, key = sign.Subject(p.Binding()), keyFingerprint(p.PublicKey)
	case appstate.ActionRevokePasskey:
		params, err := revokeParams(ps, s.trust(), body.Passkey, by.ID)
		if err != nil {
			httpError(w, http.StatusConflict, err.Error())
			return
		}
		subject = sign.Subject(params)
		if p, ok := ps.Get(body.Passkey); ok {
			key = keyFingerprint(p.PublicKey)
		}
	default:
		httpError(w, http.StatusBadRequest, "action must be owner, add-owner or revoke-passkey")
		return
	}
	in := sign.ServerIntent{Action: body.Action, Server: rpID, Subject: subject, Nonce: sign.NewNonce(), Time: time.Now().UTC().Format("2006-01-02T15:04:05Z"), Credential: by.ID}
	s.mu.Lock()
	s.sweep(time.Now())
	s.ownerNonces[in.Nonce] = ownerNonce{expires: time.Now().Add(ownerNonceTTL), action: in.Action, subject: subject, credential: by.ID}
	s.mu.Unlock()
	writeJSON(w, map[string]any{
		"intent":  in,
		"key":     key,
		"message": base64.StdEncoding.EncodeToString(in.Message()),
		"publicKey": map[string]any{
			"challenge":        b64u(in.Challenge()),
			"rpId":             rpID,
			"allowCredentials": []map[string]string{{"type": "public-key", "id": by.ID}},
			"userVerification": "required",
			"timeout":          int(ownerNonceTTL / time.Millisecond),
		},
	})
}

func sha256Hex(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && strings.ToLower(s) == s
}

// requestPath is the path and query a human() request is confirmed for.
func requestPath(r *http.Request) string {
	if r.URL.RawQuery != "" {
		return r.URL.Path + "?" + r.URL.RawQuery
	}
	return r.URL.Path
}

func humanSubject(method, path, bodySHA string) string {
	return sign.Subject(map[string]string{"method": method, "path": path, "body_sha256": bodySHA})
}

// revokeParams are what revoking id signs: the owners that would lose their grant with it,
// kept by the signer, who must not depend on id.
func revokeParams(ps *appstate.Passkeys, t appstate.Trust, id, signer string) (map[string]string, error) {
	if id == signer {
		return nil, errors.New("revoke this passkey from another owner's passkey, or from the Mac")
	}
	if _, ok := ps.Get(id); !ok {
		return nil, appstate.ErrNotFound
	}
	before := ps.Owners(t)
	after := without(ps, id).Owners(t)
	if before[signer] && !after[signer] {
		return nil, errors.New("your passkey was confirmed through this one: revoke it from another owner's passkey")
	}
	var keep []*appstate.Passkey
	for i := range ps.Passkeys {
		p := &ps.Passkeys[i]
		if before[p.ID] && !after[p.ID] && p.ID != id {
			keep = append(keep, p)
		}
	}
	return map[string]string{"credential": id, "keep": appstate.KeepList(keep)}, nil
}

func softRevoked(ps *appstate.Passkeys, p *appstate.Passkey) bool {
	for _, x := range ps.Soft {
		if x.Credential == p.ID || x.PublicKeySHA256 == appstate.PublicKeySHA256(p.PublicKey) {
			return true
		}
	}
	return false
}

func without(ps *appstate.Passkeys, id string) *appstate.Passkeys {
	out := &appstate.Passkeys{Soft: ps.Soft}
	for _, p := range ps.Passkeys {
		if p.ID != id {
			out.Passkeys = append(out.Passkeys, p)
		}
	}
	return out
}

// ownerAssertion reads the signed server intent a request carries in its header.
func ownerAssertion(r *http.Request) (*sign.SignedServer, error) {
	h := r.Header.Get(ownerHeader)
	if h == "" {
		return nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(h)
	if err != nil {
		return nil, errors.New(ownerHeader + " must be base64url JSON")
	}
	var sg sign.SignedServer
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sg); err != nil {
		return nil, errors.New(ownerHeader + ": " + err.Error())
	}
	return &sg, nil
}

// verifyOwner checks an owner's signed server intent for action and subject: a nonce this
// server issued (spent here), fresh, by a passkey that is an owner now, at this address.
func (s *Server) verifyOwner(r *http.Request, sg *sign.SignedServer, action, subject string) (*appstate.Passkey, error) {
	in := sg.Intent
	now := time.Now()
	s.mu.Lock()
	s.sweep(now)
	n, ok := s.ownerNonces[in.Nonce]
	delete(s.ownerNonces, in.Nonce)
	s.mu.Unlock()
	if !ok {
		return nil, errors.New("unknown or used challenge: ask for a new one")
	}
	if n.action != action || in.Action != action || n.subject != subject || in.Subject != subject || n.credential != in.Credential {
		return nil, errors.New("the passkey signed for something else")
	}
	if err := in.Fresh(now, ownerNonceTTL); err != nil {
		return nil, err
	}
	rpID, err := rpIDOf(r)
	if err != nil {
		return nil, err
	}
	ps, valid, err := s.owners()
	if err != nil {
		return nil, err
	}
	p, ok := ps.Get(in.Credential)
	if !ok || !valid[p.ID] {
		return nil, errors.New("that passkey is not an owner of this server")
	}
	if p.RPID != rpID {
		return nil, errors.New("that passkey was registered at another address")
	}
	pub, err := sign.ParsePublicKey(p.PublicKey)
	if err != nil {
		return nil, err
	}
	if err := sg.Verify(pub, rpID); err != nil {
		return nil, err
	}
	used := *p
	_ = s.Store.UpdatePasskeys(func(ps *appstate.Passkeys) error {
		if q, ok := ps.Get(used.ID); ok {
			q.LastUsed = now.UTC().Truncate(time.Second)
			if c, err := sg.SignCount(); err == nil && c > q.SignCount {
				q.SignCount = c
			}
		}
		return nil
	})
	return &used, nil
}

// grantPasskey makes a waiting owner passkey an owner, with another owner's signature.
func (s *Server) grantPasskey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sg, err := ownerAssertion(r)
	if err != nil || sg == nil {
		httpError(w, http.StatusForbidden, "confirm with an owner's passkey")
		return
	}
	ps, valid, err := s.owners()
	if err != nil {
		fail(w, err)
		return
	}
	p, ok := ps.Get(id)
	if !ok || !p.Owner || valid[id] || !s.madeHere(p) {
		httpError(w, http.StatusConflict, "that passkey is not waiting to become an owner")
		return
	}
	params := p.Binding()
	by, err := s.verifyOwner(r, sg, appstate.ActionAddOwner, sign.Subject(params))
	if err != nil {
		httpError(w, http.StatusForbidden, "passkey refused: "+err.Error())
		return
	}
	err = s.Store.UpdatePasskeys(func(ps *appstate.Passkeys) error {
		q, ok := ps.Get(id)
		if !ok || !sameKey(q, p) {
			return appstate.ErrNotFound
		}
		q.Grant = &appstate.Grant{Kind: appstate.GrantOwner, By: by.ID, Params: params, Signed: sg}
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	s.broadcast(sseEvent{"passkeys", nil})
	w.WriteHeader(http.StatusNoContent)
}

func sameKey(a, b *appstate.Passkey) bool {
	return a.ID == b.ID && a.PublicKey == b.PublicKey && a.RPID == b.RPID
}

// anchorPasskey makes a waiting owner passkey the Mac's: the Mac app signs, with its Secure
// Enclave key behind Touch ID, the intent anchorIntent prepared.
func (s *Server) anchorPasskey(w http.ResponseWriter, r *http.Request) {
	if id := identityOf(r); id.Via != viaToken || !id.Shell {
		httpError(w, http.StatusForbidden, "only the Truehand Mac app confirms a passkey with this Mac")
		return
	}
	var body struct {
		Signed sign.SignedServer `json:"signed"`
	}
	if !decode(w, r, &body) {
		return
	}
	pid := r.PathValue("id")
	sg := body.Signed
	s.mu.Lock()
	n, ok := s.ownerNonces[sg.Intent.Nonce]
	delete(s.ownerNonces, sg.Intent.Nonce)
	s.mu.Unlock()
	if !ok || n.action != appstate.ActionAnchorOwner || n.credential != pid || sg.Intent.Subject != n.subject {
		httpError(w, http.StatusForbidden, "unknown or used challenge: ask for a new one")
		return
	}
	var ownerErr error
	err := s.Store.UpdatePasskeys(func(ps *appstate.Passkeys) error {
		p, ok := ps.Get(pid)
		if !ok || !p.Owner || !s.madeHere(p) {
			return appstate.ErrNotFound
		}
		old := p.Grant
		p.Grant = &appstate.Grant{Kind: appstate.GrantMac, Signed: &sg}
		// The signature is checked here; whether the passkey acts as owner also waits for
		// the Mac's revocations, which may come later.
		t := s.trust()
		if errors.Is(t.Broken, errNoMacRevocations) {
			t.Broken = nil
		}
		if ownerErr = ps.GrantError(pid, t); ownerErr != nil {
			p.Grant = old
			return errBadRequest("signature refused: " + ownerErr.Error())
		}
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	s.broadcast(sseEvent{"passkeys", nil})
	w.WriteHeader(http.StatusNoContent)
}

// anchorIntent is what the Mac app signs to make a waiting passkey an owner.
func (s *Server) anchorIntent(w http.ResponseWriter, r *http.Request) {
	if id := identityOf(r); id.Via != viaToken || !id.Shell {
		httpError(w, http.StatusForbidden, "only the Truehand Mac app confirms a passkey with this Mac")
		return
	}
	ps, valid, err := s.owners()
	if err != nil {
		fail(w, err)
		return
	}
	p, ok := ps.Get(r.PathValue("id"))
	if !ok || !p.Owner || valid[p.ID] || !s.madeHere(p) {
		httpError(w, http.StatusConflict, "that passkey is not waiting to become an owner")
		return
	}
	in := sign.ServerIntent{Action: appstate.ActionAnchorOwner, Server: p.RPID, Subject: sign.Subject(p.Binding()), Nonce: sign.NewNonce(), Time: time.Now().UTC().Format("2006-01-02T15:04:05Z"), Credential: p.ID}
	s.mu.Lock()
	s.ownerNonces[in.Nonce] = ownerNonce{expires: time.Now().Add(ownerNonceTTL), action: in.Action, subject: in.Subject, credential: p.ID}
	s.mu.Unlock()
	device := ""
	if a, err := s.Store.Auth(); err == nil {
		for _, d := range a.Devices {
			if d.ID == p.Device {
				device = d.Name
			}
		}
	}
	writeJSON(w, map[string]any{"intent": in, "message": base64.StdEncoding.EncodeToString(in.Message()), "label": p.Label, "key": keyFingerprint(p.PublicKey),
		"rpId": p.RPID, "publicKey": p.PublicKey, "device": device, "synced": p.BackupState})
}

// deletePasskey removes a passkey. From an owner's passkey (revoke-passkey) the owners that
// depended on it are kept, granted again by the signer; from the Mac they may be orphaned.
func (s *Server) deletePasskey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sg, err := ownerAssertion(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	if sg != nil && sg.Intent.Action == appstate.ActionRevokePasskey {
		ps, err := s.Store.Passkeys()
		if err != nil {
			fail(w, err)
			return
		}
		params, err := revokeParams(ps, s.trust(), id, sg.Intent.Credential)
		if err != nil {
			httpError(w, http.StatusConflict, err.Error())
			return
		}
		by, err := s.verifyOwner(r, sg, appstate.ActionRevokePasskey, sign.Subject(params))
		if err != nil {
			httpError(w, http.StatusForbidden, "passkey refused: "+err.Error())
			return
		}
		keep := strings.Split(params["keep"], ",")
		err = s.Store.UpdatePasskeys(func(ps *appstate.Passkeys) error {
			if p, ok := ps.Get(id); ok && p.Owner {
				ps.Soft = append(ps.Soft, appstate.RevocationOf(p, time.Now()))
			}
			if !ps.Remove(id) {
				return appstate.ErrNotFound
			}
			for i := range ps.Passkeys {
				p := &ps.Passkeys[i]
				if contains(keep, p.ID+":"+appstate.PublicKeySHA256(p.PublicKey)) {
					p.Grant = &appstate.Grant{Kind: appstate.GrantOwner, By: by.ID, Params: params, Signed: sg}
				}
			}
			return nil
		})
		if err != nil {
			fail(w, err)
			return
		}
		s.broadcast(sseEvent{"passkeys", nil})
		writeJSON(w, map[string]any{"orphaned": []string{}})
		return
	}
	if !s.human(w, r, "Truehand: remove a passkey from this server", nil) {
		return
	}
	var orphaned []string
	err = s.Store.UpdatePasskeys(func(ps *appstate.Passkeys) error {
		before := ps.Owners(s.trust())
		if p, ok := ps.Get(id); ok && p.Owner {
			ps.Soft = append(ps.Soft, appstate.RevocationOf(p, time.Now()))
		}
		if !ps.Remove(id) {
			return appstate.ErrNotFound
		}
		after := ps.Owners(s.trust())
		for o := range before {
			if !after[o] && o != id {
				orphaned = append(orphaned, o)
			}
		}
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	s.broadcast(sseEvent{"passkeys", nil})
	if orphaned == nil {
		orphaned = []string{}
	}
	writeJSON(w, map[string]any{"orphaned": orphaned})
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// bodyHash reads an unsafe request's body to hash it for an owner's assertion, and puts it
// back for the handler.
func bodyHash(r *http.Request) (string, error) {
	if r.Body == nil {
		h := sha256.Sum256(nil)
		return hex.EncodeToString(h[:]), nil
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	r.Body.Close()
	if err != nil {
		return "", err
	}
	if len(b) > 1<<20 {
		return "", errors.New("body too large")
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
