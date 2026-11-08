package serve

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spaghetticoders/truehand/internal/appstate"
	"github.com/spaghetticoders/truehand/internal/appstate/webauthntest"
	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/sign"
)

const tsHost = "box.tail1234.ts.net"

// phone is a browser reaching the server through tailscale serve, with its own passkey.
type phone struct {
	e    *env
	jar  *cookiejar.Jar
	auth *webauthntest.Authenticator
	host string
	hdr  map[string]string
}

func (e *env) phone(rsa bool) *phone {
	jar, _ := cookiejar.New(nil)
	a := webauthntest.NewES256(tsHost)
	if rsa {
		a = webauthntest.NewRS256(tsHost)
	}
	return &phone{e: e, jar: jar, auth: a, host: tsHost}
}

func (p *phone) do(method, path, body string, hdr map[string]string) (int, []byte) {
	p.e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, p.e.srv.URL+path, rd)
	req.Host = p.host
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Origin", "https://"+p.host)
	if body != "" {
		req.Header.Set("Content-Type", jsonType)
	}
	for k, v := range p.hdr {
		req.Header.Set(k, v)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	u := *req.URL
	u.Host, u.Scheme = p.host, "https"
	for _, c := range p.jar.Cookies(&u) {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		p.e.t.Fatal(err)
	}
	defer resp.Body.Close()
	p.jar.SetCookies(&u, resp.Cookies())
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (p *phone) json(method, path, body string, hdr map[string]string, out any) int {
	p.e.t.Helper()
	code, b := p.do(method, path, body, hdr)
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			p.e.t.Fatalf("%s %s: %d %s", method, path, code, b)
		}
	}
	return code
}

// register enrols the phone's passkey with code; it returns the status of the last step.
func (p *phone) register(code string) (int, passkeyView) {
	p.e.t.Helper()
	var opts struct {
		Ceremony  string `json:"ceremony"`
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	b, _ := json.Marshal(map[string]string{"code": code, "label": "S25"})
	if c := p.json("POST", "/api/passkeys/register/options", string(b), nil, &opts); c != 200 {
		return c, passkeyView{}
	}
	ch, _ := base64.RawURLEncoding.DecodeString(opts.PublicKey.Challenge)
	att, cdj := p.auth.Create(ch)
	b, _ = json.Marshal(map[string]any{"ceremony": opts.Ceremony, "attestationObject": att, "clientDataJSON": cdj})
	var v passkeyView
	c := p.json("POST", "/api/passkeys/register", string(b), nil, &v)
	return c, v
}

// signed asks for the challenge of action and signs it with the phone's passkey.
func (p *phone) signed(action string, extra map[string]string) (int, string) {
	p.e.t.Helper()
	req := map[string]string{"credential": p.auth.CredentialID(), "action": action}
	for k, v := range extra {
		req[k] = v
	}
	b, _ := json.Marshal(req)
	var ch struct {
		Intent sign.ServerIntent `json:"intent"`
	}
	code, out := p.do("POST", "/api/owner/challenge", string(b), nil)
	if code != 200 {
		return code, string(out)
	}
	json.Unmarshal(out, &ch)
	ad, cd, sig := p.auth.Assert(ch.Intent.Challenge())
	sg := sign.SignedServer{Intent: ch.Intent, Format: sign.FormatWebAuthn, Sig: sig, AuthenticatorData: ad, ClientDataJSON: cd}
	j, _ := json.Marshal(sg)
	return 200, base64.RawURLEncoding.EncodeToString(j)
}

func bodySHA(body string) string {
	h := sha256.Sum256([]byte(body))
	return hex.EncodeToString(h[:])
}

// asOwner makes a human request confirmed by the phone's passkey.
func (p *phone) asOwner(method, path, body string, out any) int {
	p.e.t.Helper()
	code, h := p.signed(appstate.ActionOwner, map[string]string{"method": method, "path": path, "bodySha256": bodySHA(body)})
	if code != 200 {
		p.e.t.Fatalf("owner challenge: %d %s", code, h)
	}
	return p.json(method, path, body, map[string]string{ownerHeader: h}, out)
}

// headless turns the env's server into a Linux one: no Mac app, an anchor it reads.
func (e *env) headless() *appstate.Anchor {
	a := &appstate.Anchor{Version: 1, UID: os.Getuid()}
	e.s.Headless = true
	e.s.ShellSecretHash = ""
	e.s.SetHosts([]string{tsHost})
	e.s.ReadAnchor = func() (*appstate.Anchor, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		c := *a
		return &c, nil
	}
	return a
}

func (e *env) setAnchor(a *appstate.Anchor, fn func(*appstate.Anchor)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	fn(a)
}

// bootstrap does what `sudo truehand passkey bootstrap` does around the phone's registration.
func (e *env) bootstrap(a *appstate.Anchor, p *phone) {
	e.t.Helper()
	code := "ABCDEFGHJKLMNPQR"
	e.setAnchor(a, func(a *appstate.Anchor) {
		a.CodeSHA256, a.Expires, a.Owner = appstate.HashSecret(code), time.Now().Add(10*time.Minute), nil
	})
	if c, v := p.register(code); c != 200 || !v.Owner || v.OwnerValid || v.Pending != "anchor" {
		e.t.Fatalf("bootstrap register: %d %+v", c, v)
	}
	ps, _ := e.s.Store.Passkeys()
	pk, ok := ps.Get(p.auth.CredentialID())
	if !ok || !appstate.CodeProofOK(code, pk) {
		e.t.Fatal("registered passkey has no valid code proof")
	}
	e.setAnchor(a, func(a *appstate.Anchor) {
		a.CodeSHA256, a.Expires = "", time.Time{}
		a.Owner = &appstate.AnchorOwner{Credential: pk.ID, PublicKeySHA256: appstate.PublicKeySHA256(pk.PublicKey), RPID: pk.RPID}
	})
}

func TestHeadlessNeedsAnOwner(t *testing.T) {
	e := newEnv(t, false)
	e.headless()
	p := e.phone(false)
	// Nothing to sign in with yet: the shell's credentials are not there, and the token
	// does not stand for a person.
	body := `{"planSha256":"` + planSHA(t, e) + `"}`
	resp, b := e.req("POST", "/api/projects/"+e.pid+"/tasks/T-1/actions/approve-plan", body, asToken)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(b), codeOwnerNeeded) {
		t.Fatalf("approve without owner = %d %s", resp.StatusCode, b)
	}
	if q := e.s.queuedActions(); len(q) != 0 {
		t.Fatalf("headless server queued %v", q)
	}
	if e.authCount() != 0 {
		t.Fatal("headless server asked the OS")
	}
	// No enrolment code can be released either.
	if c := e.code("POST", "/api/passkeys/enroll-code", `{"owner":true}`, asToken); c != http.StatusConflict {
		t.Fatalf("enroll-code without owner = %d", c)
	}
	// Nor can agents be set to work.
	if c := e.code("POST", "/api/projects/"+e.pid+"/sessions", "", asToken); c != http.StatusConflict {
		t.Fatalf("session without owner = %d", c)
	}
	if c, _ := p.register("WRONGCODE"); c != http.StatusUnauthorized {
		t.Fatalf("register with a wrong code = %d", c)
	}
}

func planSHA(t *testing.T, e *env) string {
	t.Helper()
	ws, err := core.Open(e.root)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(ws.Store.PlanPath("T-1"))
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestPasskeyBootstrapAndApprove(t *testing.T) {
	e := newEnv(t, false)
	a := e.headless()
	s25 := e.phone(false)
	e.bootstrap(a, s25)
	if !e.s.HasOwner() {
		t.Fatal("anchored passkey is not an owner")
	}
	if c := e.code("POST", "/api/projects/"+e.pid+"/sessions", "", asToken); c != http.StatusCreated {
		t.Fatalf("session with an owner = %d", c)
	}
	// Registering paired the phone: it now has a device cookie.
	var me map[string]any
	if c := s25.json("GET", "/api/me", "", nil, &me); c != 200 || me["headless"] != true || me["owner"] != true {
		t.Fatalf("/api/me after registering = %d %v", c, me)
	}

	// Without the passkey, the phone's cookie is not a person.
	path := "/api/projects/" + e.pid + "/tasks/T-1/actions/approve-plan"
	body := `{"planSha256":"` + planSHA(t, e) + `"}`
	if c := s25.json("POST", path, body, nil, nil); c != http.StatusConflict {
		t.Fatalf("approve with cookie only = %d", c)
	}
	var res actResult
	if c := s25.asOwner("POST", path, body, &res); c != 200 || !res.OK {
		t.Fatalf("approve with passkey = %d %+v", c, res)
	}
	ws, _ := core.Open(e.root)
	task, _ := ws.Store.Load("T-1")
	if task.State != board.Approved {
		t.Fatalf("task is %s", task.State)
	}
	aps, _ := ws.Store.Approvals("T-1")
	if len(aps) == 0 || aps[len(aps)-1].Via != "passkey" {
		t.Fatalf("approval via = %+v", aps)
	}
	if e.authCount() != 0 {
		t.Fatal("the OS was asked")
	}
}

func TestOwnerAssertionIsBound(t *testing.T) {
	e := newEnv(t, false)
	a := e.headless()
	s25 := e.phone(false)
	e.bootstrap(a, s25)
	path := "/api/passkeys/enroll-code"
	body := `{"owner":false}`

	_, h := s25.signed(appstate.ActionOwner, map[string]string{"method": "POST", "path": path, "bodySha256": bodySHA(body)})
	// Another body on the same path.
	if c := s25.json("POST", path, `{"owner":true}`, map[string]string{ownerHeader: h}, nil); c != http.StatusForbidden {
		t.Fatalf("assertion with another body = %d", c)
	}
	// Spent by the refusal: single use.
	if c := s25.json("POST", path, body, map[string]string{ownerHeader: h}, nil); c != http.StatusForbidden {
		t.Fatalf("assertion reused = %d", c)
	}
	_, h = s25.signed(appstate.ActionOwner, map[string]string{"method": "POST", "path": path, "bodySha256": bodySHA(body)})
	if c := s25.json("POST", "/api/pair/code", body, map[string]string{ownerHeader: h}, nil); c != http.StatusForbidden {
		t.Fatalf("assertion on another path = %d", c)
	}
	_, h = s25.signed(appstate.ActionOwner, map[string]string{"method": "POST", "path": path, "bodySha256": bodySHA(body)})
	if c := s25.json("POST", path+"?owner=1", body, map[string]string{ownerHeader: h}, nil); c != http.StatusForbidden {
		t.Fatalf("assertion with a query it was not signed for = %d", c)
	}
	_, h = s25.signed(appstate.ActionOwner, map[string]string{"method": "POST", "path": path, "bodySha256": bodySHA(body)})
	if c := s25.json("POST", path, body, map[string]string{ownerHeader: h}, nil); c != 200 {
		t.Fatalf("good assertion = %d", c)
	}
	if c := s25.json("POST", path, body, map[string]string{ownerHeader: h}, nil); c != http.StatusForbidden {
		t.Fatalf("good assertion replayed = %d", c)
	}
	// Signed by a key that is not the passkey's.
	_, h = s25.signed(appstate.ActionOwner, map[string]string{"method": "POST", "path": path, "bodySha256": bodySHA(body)})
	j, _ := base64.RawURLEncoding.DecodeString(h)
	var sg sign.SignedServer
	json.Unmarshal(j, &sg)
	other := webauthntest.NewES256(tsHost)
	sg.AuthenticatorData, sg.ClientDataJSON, sg.Sig = other.Assert(sg.Intent.Challenge())
	j, _ = json.Marshal(sg)
	if c := s25.json("POST", path, body, map[string]string{ownerHeader: base64.RawURLEncoding.EncodeToString(j)}, nil); c != http.StatusForbidden {
		t.Fatalf("assertion by another key = %d", c)
	}
	// Over plain http the server cannot be a relying party.
	resp, _ := e.req("POST", "/api/owner/challenge", `{"credential":"`+s25.auth.CredentialID()+`","action":"owner","method":"POST","path":"/api/x","bodySha256":"`+bodySHA("")+`"}`, asToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("challenge over http = %d", resp.StatusCode)
	}
}

func TestForgedOwnerInTheFile(t *testing.T) {
	e := newEnv(t, false)
	a := e.headless()
	s25 := e.phone(false)
	e.bootstrap(a, s25)
	// An agent, running as the user, writes its own key in as an owner, granted by itself.
	evil := e.phone(false)
	evil.jar = s25.jar
	e.s.Store.UpdatePasskeys(func(ps *appstate.Passkeys) error {
		p := appstate.Passkey{ID: evil.auth.CredentialID(), PublicKey: evil.auth.PublicKeyPEM(), Alg: -7, RPID: tsHost, Owner: true, Label: "evil"}
		params := p.Binding()
		sg := evil.auth.SignServer(sign.ServerIntent{Action: appstate.ActionAddOwner, Subject: sign.Subject(params), Nonce: sign.NewNonce(), Time: time.Now().UTC().Format(time.RFC3339)})
		p.Grant = &appstate.Grant{Kind: appstate.GrantOwner, By: p.ID, Params: params, Signed: &sg}
		ps.Passkeys = append(ps.Passkeys, p)
		return nil
	})
	if c, out := evil.signed(appstate.ActionOwner, map[string]string{"method": "POST", "path": "/api/passkeys/enroll-code", "bodySha256": bodySHA("{}")}); c != http.StatusForbidden {
		t.Fatalf("forged owner got a challenge: %d %s", c, out)
	}
	// Or claims the anchor.
	e.s.Store.UpdatePasskeys(func(ps *appstate.Passkeys) error {
		p, _ := ps.Get(evil.auth.CredentialID())
		p.Grant = &appstate.Grant{Kind: appstate.GrantAnchor}
		return nil
	})
	if c, _ := evil.signed(appstate.ActionOwner, map[string]string{"method": "POST", "path": "/api/passkeys/enroll-code", "bodySha256": bodySHA("{}")}); c != http.StatusForbidden {
		t.Fatalf("passkey claiming the anchor got a challenge: %d", c)
	}
	// Deleting the real one only locks the owner out.
	e.s.Store.UpdatePasskeys(func(ps *appstate.Passkeys) error {
		ps.Remove(s25.auth.CredentialID())
		return nil
	})
	if e.s.HasOwner() {
		t.Fatal("an owner survived without a grant")
	}
}

func TestSecondOwnerAndRevocation(t *testing.T) {
	e := newEnv(t, false)
	a := e.headless()
	s25 := e.phone(false)
	e.bootstrap(a, s25)

	// The S25 releases an owner code; an iPhone (RS256 here, like Windows Hello) registers.
	var code struct {
		Code string `json:"code"`
	}
	if c := s25.asOwner("POST", "/api/passkeys/enroll-code", `{"owner":true}`, &code); c != 200 || code.Code == "" {
		t.Fatalf("enroll-code = %d", c)
	}
	iphone := e.phone(true)
	c, v := iphone.register(code.Code)
	if c != 200 || !v.Owner || v.OwnerValid || v.Pending != "owner" || v.Alg != "RS256" {
		t.Fatalf("iphone register: %d %+v", c, v)
	}
	// The code is spent.
	if c, _ := e.phone(false).register(code.Code); c != http.StatusUnauthorized {
		t.Fatalf("code reused = %d", c)
	}
	// The iPhone cannot confirm itself.
	if c, _ := iphone.signed(appstate.ActionAddOwner, map[string]string{"passkey": iphone.auth.CredentialID()}); c != http.StatusForbidden {
		t.Fatalf("self-confirmation challenge = %d", c)
	}
	_, h := s25.signed(appstate.ActionAddOwner, map[string]string{"passkey": iphone.auth.CredentialID()})
	if c := s25.json("POST", "/api/passkeys/"+iphone.auth.CredentialID()+"/grant", "", map[string]string{ownerHeader: h}, nil); c != http.StatusNoContent {
		t.Fatalf("grant = %d", c)
	}
	_, valid, _ := e.s.owners()
	if !valid[iphone.auth.CredentialID()] {
		t.Fatal("granted passkey is not an owner")
	}
	// A third owner, granted by the iPhone.
	if c := iphone.asOwner("POST", "/api/passkeys/enroll-code", `{"owner":true}`, &code); c != 200 {
		t.Fatalf("enroll-code from iphone = %d", c)
	}
	key := e.phone(false)
	key.register(code.Code)
	_, h = iphone.signed(appstate.ActionAddOwner, map[string]string{"passkey": key.auth.CredentialID()})
	if c := iphone.json("POST", "/api/passkeys/"+key.auth.CredentialID()+"/grant", "", map[string]string{ownerHeader: h}, nil); c != http.StatusNoContent {
		t.Fatalf("grant by iphone = %d", c)
	}
	// The key depends on the iPhone: revoking the iPhone from the S25 keeps it.
	if c, _ := key.signed(appstate.ActionRevokePasskey, map[string]string{"passkey": iphone.auth.CredentialID()}); c != http.StatusConflict {
		t.Fatalf("revoke from a dependent = %d", c)
	}
	_, h = s25.signed(appstate.ActionRevokePasskey, map[string]string{"passkey": iphone.auth.CredentialID()})
	if c := s25.json("DELETE", "/api/passkeys/"+iphone.auth.CredentialID(), "", map[string]string{ownerHeader: h}, nil); c != 200 {
		t.Fatalf("revoke = %d", c)
	}
	ps, valid, _ := e.s.owners()
	if _, ok := ps.Get(iphone.auth.CredentialID()); ok || !valid[key.auth.CredentialID()] || !valid[s25.auth.CredentialID()] {
		t.Fatalf("after revoke: owners %v", valid)
	}
}

func TestEnrollCodeLimits(t *testing.T) {
	e := newEnv(t, false)
	a := e.headless()
	p := e.phone(false)
	for i := 0; i < pairMaxFails; i++ {
		p.register("NOPE" + string(rune('A'+i)))
	}
	if c, _ := p.register("ANYTHING"); c != http.StatusTooManyRequests {
		t.Fatalf("after %d wrong codes = %d", pairMaxFails, c)
	}
	// Another client (tailscale serve names it) is not shut out by the noisy one.
	code := "ABCDEFGHJKLMNPQR"
	e.setAnchor(a, func(a *appstate.Anchor) {
		a.CodeSHA256, a.Expires = appstate.HashSecret(code), time.Now().Add(time.Minute)
	})
	other := e.phone(false)
	other.hdr = map[string]string{"X-Forwarded-For": "100.64.0.9"}
	if c, _ := other.register(code); c != 200 {
		t.Fatalf("right code from another source = %d", c)
	}
}

func TestMacEnrollAndAnchor(t *testing.T) {
	e := newEnv(t, false)
	e.s.SetHosts([]string{tsHost})
	e.s.ReadAnchor = func() (*appstate.Anchor, error) { return nil, nil }
	var code struct {
		Code string `json:"code"`
	}
	if c := e.json("POST", "/api/passkeys/enroll-code", `{"owner":true}`, asShell, &code); c != 200 || e.authCount() != 1 {
		t.Fatalf("mac enroll-code = %d (auths %d)", c, e.authCount())
	}
	// Without the shell's secret a phone is refused, not queued.
	if c := e.code("POST", "/api/passkeys/enroll-code", `{"owner":true}`, asToken); c != http.StatusForbidden {
		t.Fatalf("enroll-code without shell = %d", c)
	}
	p := e.phone(false)
	if c, v := p.register(code.Code); c != 200 || v.Pending != "mac" {
		t.Fatalf("register = %d %+v", c, v)
	}
	if e.s.HasOwner() {
		t.Fatal("owner before the Mac confirmed")
	}
	mac := mustKey(t)
	e.s.MacKey = &mac.PublicKey
	var ai struct {
		Intent sign.ServerIntent `json:"intent"`
	}
	if c := e.json("GET", "/api/passkeys/"+p.auth.CredentialID()+"/anchor-intent", "", asToken, nil); c != http.StatusForbidden {
		t.Fatalf("anchor-intent without shell = %d", c)
	}
	e.json("GET", "/api/passkeys/"+p.auth.CredentialID()+"/anchor-intent", "", asShell, &ai)
	sg := webauthntest.SignES256(mac, ai.Intent)
	b, _ := json.Marshal(map[string]any{"signed": sg})
	if c := e.code("POST", "/api/passkeys/"+p.auth.CredentialID()+"/anchor", string(b), asShell); c != http.StatusNoContent {
		t.Fatalf("anchor = %d", c)
	}
	if e.s.HasOwner() {
		t.Fatal("owner before the Mac app sent its revocations")
	}
	if c := e.code("POST", "/api/shell/revoked", `{"revoked":[]}`, asShell); c != http.StatusNoContent {
		t.Fatalf("shell revoked = %d", c)
	}
	if !e.s.HasOwner() {
		t.Fatal("mac-anchored passkey is not an owner")
	}
	// A server started with this passkey in the app's list (stdin) never takes it for an
	// owner, from the start.
	ps, _ := e.s.Store.Passkeys()
	pk, _ := ps.Get(p.auth.CredentialID())
	e.s.SetMacRevoked([]appstate.Revocation{appstate.RevocationOf(pk, time.Now())})
	if e.s.HasOwner() {
		t.Fatal("revoked in the app's list, still an owner")
	}
	e.s.SetMacRevoked(nil)
	// A signature by another key is refused.
	other := e.phone(false)
	e.json("POST", "/api/passkeys/enroll-code", `{"owner":true}`, asShell, &code)
	other.register(code.Code)
	e.json("GET", "/api/passkeys/"+other.auth.CredentialID()+"/anchor-intent", "", asShell, &ai)
	sg = webauthntest.SignES256(mustKey(t), ai.Intent)
	b, _ = json.Marshal(map[string]any{"signed": sg})
	if c := e.code("POST", "/api/passkeys/"+other.auth.CredentialID()+"/anchor", string(b), asShell); c != http.StatusBadRequest {
		t.Fatalf("anchor by another key = %d", c)
	}
}

func TestAPIHasStrictCSP(t *testing.T) {
	e := newEnv(t, false)
	resp, _ := e.req("GET", "/api/version", "", asToken)
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "sandbox") {
		t.Fatalf("api CSP = %q", csp)
	}
	resp, _ = e.req("GET", "/", "", anon)
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "sandbox") || strings.Contains(csp, "unsafe-eval") {
		t.Fatalf("page CSP = %q", csp)
	}
}

func mustKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestChallengeCarriesTheMessage(t *testing.T) {
	e := newEnv(t, false)
	a := e.headless()
	s25 := e.phone(false)
	e.bootstrap(a, s25)
	var ch struct {
		Intent    sign.ServerIntent `json:"intent"`
		Message   string            `json:"message"`
		PublicKey struct {
			Challenge string `json:"challenge"`
			RPID      string `json:"rpId"`
		} `json:"publicKey"`
	}
	b, _ := json.Marshal(map[string]string{"credential": s25.auth.CredentialID(), "action": "owner", "method": "POST", "path": "/api/pair/code", "bodySha256": bodySHA("")})
	s25.json("POST", "/api/owner/challenge", string(b), nil, &ch)
	msg, _ := base64.StdEncoding.DecodeString(ch.Message)
	h := sha256.Sum256(msg)
	if string(msg) != string(ch.Intent.Message()) || ch.PublicKey.Challenge != base64.RawURLEncoding.EncodeToString(h[:]) || ch.PublicKey.RPID != tsHost {
		t.Fatalf("challenge = %+v", ch)
	}
}

// tailscale serve on another HTTPS port: the relying party is still the host alone.
func TestPasskeyOnAnotherPort(t *testing.T) {
	e := newEnv(t, false)
	a := e.headless()
	s25 := e.phone(false)
	s25.host = tsHost + ":8443"
	s25.auth.Origin = "https://" + tsHost + ":8443"
	e.s.SetHosts([]string{tsHost})
	e.bootstrap(a, s25)
	ps, _ := e.s.Store.Passkeys()
	if p, _ := ps.Get(s25.auth.CredentialID()); p == nil || p.RPID != tsHost {
		t.Fatalf("rp_id = %+v", p)
	}
	var code struct {
		Code string `json:"code"`
	}
	if c := s25.asOwner("POST", "/api/passkeys/enroll-code", `{"owner":false}`, &code); c != 200 {
		t.Fatalf("owner action on :8443 = %d", c)
	}
}

func TestSignatureRequiredNamesThePasskey(t *testing.T) {
	e := newEnv(t, false)
	a := e.headless()
	s25 := e.phone(false)
	e.bootstrap(a, s25)
	// The repo lists the S25's passkey as approver mac.
	indented := "      " + strings.ReplaceAll(strings.TrimSpace(s25.auth.PublicKeyPEM()), "\n", "\n      ")
	cfg := "version: 0\nbase: main\napprovers:\n  - id: mac\n    name: Alice\n    rp_id: " + tsHost + "\n    public_key: |\n" + indented + "\ngates:\n  approved:\n    - check: plan_valid\n    - check: human_approval\n"
	os.WriteFile(filepath.Join(e.root, ".truehand/config.yaml"), []byte(cfg), 0o644)
	git(t, e.root, "commit", "-q", "-am", "chore: approvers")
	var out struct {
		Code         string        `json:"code"`
		ApproverKeys []approverKey `json:"approverKeys"`
	}
	path := "/api/projects/" + e.pid + "/tasks/T-1"
	s25.json("POST", path+"/actions/approve-plan", `{"planSha256":"`+planSHA(t, e)+`"}`, nil, &out)
	if out.Code != codeSignatureRequired || len(out.ApproverKeys) != 1 || out.ApproverKeys[0].CredentialID != s25.auth.CredentialID() || out.ApproverKeys[0].RPID != tsHost {
		t.Fatalf("409 = %+v", out)
	}
	var in struct {
		RPID  string              `json:"rpId"`
		Allow []map[string]string `json:"allowCredentials"`
	}
	s25.json("POST", path+"/intent", `{"action":"approve-plan","approver":"mac"}`, nil, &in)
	if in.RPID != tsHost || len(in.Allow) != 1 || in.Allow[0]["id"] != s25.auth.CredentialID() {
		t.Fatalf("intent = %+v", in)
	}
}

func TestRevocations(t *testing.T) {
	e := newEnv(t, false)
	a := e.headless()
	s25 := e.phone(false)
	e.bootstrap(a, s25)
	var code struct {
		Code string `json:"code"`
	}
	s25.asOwner("POST", "/api/passkeys/enroll-code", `{"owner":true}`, &code)
	lost := e.phone(false)
	lost.register(code.Code)
	_, h := s25.signed(appstate.ActionAddOwner, map[string]string{"passkey": lost.auth.CredentialID()})
	s25.json("POST", "/api/passkeys/"+lost.auth.CredentialID()+"/grant", "", map[string]string{ownerHeader: h}, nil)
	backup, _ := e.s.Store.Passkeys()

	// Revoked from the S25: soft, listed as such.
	_, h = s25.signed(appstate.ActionRevokePasskey, map[string]string{"passkey": lost.auth.CredentialID()})
	if c := s25.json("DELETE", "/api/passkeys/"+lost.auth.CredentialID(), "", map[string]string{ownerHeader: h}, nil); c != 200 {
		t.Fatalf("revoke = %d", c)
	}
	var list struct {
		Revocations []revocationView `json:"revocations"`
	}
	s25.json("GET", "/api/passkeys", "", nil, &list)
	if len(list.Revocations) != 1 || list.Revocations[0].Revoked != "soft" || list.Revocations[0].ID != lost.auth.CredentialID() || list.Revocations[0].Key != keyFingerprint(lost.auth.PublicKeyPEM()) {
		t.Fatalf("revocations = %+v", list.Revocations)
	}
	// An agent restores the old file: the lost phone is an owner again (the documented limit)...
	e.s.Store.UpdatePasskeys(func(ps *appstate.Passkeys) error { *ps = *backup; return nil })
	if _, valid, _ := e.s.owners(); !valid[lost.auth.CredentialID()] {
		t.Fatal("expected the soft revocation to be undone by restoring the file")
	}
	// ...until root's list names it.
	pk, _ := backup.Get(lost.auth.CredentialID())
	e.s.ReadRevoked = func() ([]appstate.Revocation, error) {
		return []appstate.Revocation{appstate.RevocationOf(pk, time.Now())}, nil
	}
	if _, valid, _ := e.s.owners(); valid[lost.auth.CredentialID()] || !valid[s25.auth.CredentialID()] {
		t.Fatalf("owners with the durable revocation = %v", valid)
	}
	if c, _ := lost.signed(appstate.ActionOwner, map[string]string{"method": "POST", "path": "/api/pair/code", "bodySha256": bodySHA("")}); c != http.StatusForbidden {
		t.Fatalf("durably revoked passkey got a challenge: %d", c)
	}
	var pv struct {
		Passkeys    []passkeyView    `json:"passkeys"`
		Revocations []revocationView `json:"revocations"`
	}
	s25.json("GET", "/api/passkeys", "", nil, &pv)
	found := false
	for _, v := range pv.Passkeys {
		found = found || v.ID == lost.auth.CredentialID() && v.Revoked == "durable" && !v.OwnerValid
	}
	if !found || len(pv.Revocations) != 1 || pv.Revocations[0].Revoked != "durable" {
		t.Fatalf("view = %+v", pv)
	}
	// An unreadable list: nobody is an owner.
	e.s.ReadRevoked = func() ([]appstate.Revocation, error) { return nil, errString("not root's") }
	if e.s.HasOwner() {
		t.Fatal("owner trusted with an unreadable revocation list")
	}
}

func TestShellRevoked(t *testing.T) {
	e := newEnv(t, false)
	e.s.ReadRevoked = func() ([]appstate.Revocation, error) { return nil, nil }
	if tr := e.s.trust(); tr.Broken == nil {
		t.Fatal("a Mac server trusts owners before the app sent its revocations")
	}
	e.s.SetMacRevoked(nil)
	if tr := e.s.trust(); tr.Broken != nil {
		t.Fatalf("after the app's stdin list: %v", tr.Broken)
	}
	b := `{"revoked":[{"credential":"abc","time":"2026-11-08T00:00:00Z"}]}`
	if c := e.code("POST", "/api/shell/revoked", b, asToken); c != http.StatusForbidden {
		t.Fatalf("revoked from the token = %d", c)
	}
	if c := e.code("POST", "/api/shell/revoked", b, asShell); c != http.StatusNoContent {
		t.Fatalf("revoked from the shell = %d", c)
	}
	if tr := e.s.trust(); len(tr.Revoked) != 1 || tr.Revoked[0].Credential != "abc" {
		t.Fatalf("trust = %+v", tr.Revoked)
	}
}

// After a restart a registration waiting for confirmation is shown as expired.
func TestPendingExpiresWithTheProcess(t *testing.T) {
	e := newEnv(t, false)
	a := e.headless()
	s25 := e.phone(false)
	e.bootstrap(a, s25)
	var code struct {
		Code string `json:"code"`
	}
	s25.asOwner("POST", "/api/passkeys/enroll-code", `{"owner":true}`, &code)
	other := e.phone(false)
	if _, v := other.register(code.Code); v.Pending != "owner" {
		t.Fatalf("pending = %q", v.Pending)
	}
	e.s.mu.Lock()
	e.s.registered = map[string]registeredKey{}
	e.s.mu.Unlock()
	var pv struct {
		Passkeys []passkeyView `json:"passkeys"`
	}
	s25.json("GET", "/api/passkeys", "", nil, &pv)
	for _, v := range pv.Passkeys {
		if v.ID == other.auth.CredentialID() && v.Pending != "expired" {
			t.Fatalf("after a restart: %+v", v)
		}
	}
	var ch struct {
		Key string `json:"key"`
	}
	b, _ := json.Marshal(map[string]string{"credential": s25.auth.CredentialID(), "action": "revoke-passkey", "passkey": other.auth.CredentialID()})
	s25.json("POST", "/api/owner/challenge", string(b), nil, &ch)
	if ch.Key != keyFingerprint(other.auth.PublicKeyPEM()) {
		t.Fatalf("revoke challenge key = %q", ch.Key)
	}
}

// Root's list for another uid: no owner, and the UI is told why (not silently ignored).
func TestRevokedListOfAnotherUserFailsClosed(t *testing.T) {
	e := newEnv(t, false)
	a := e.headless()
	s25 := e.phone(false)
	e.bootstrap(a, s25)
	dir := t.TempDir()
	old, oldOK := appstate.AnchorPath, appstate.AnchorFileOK
	t.Cleanup(func() { appstate.AnchorPath, appstate.AnchorFileOK = old, oldOK })
	appstate.AnchorPath, appstate.AnchorFileOK = filepath.Join(dir, "anchor.json"), func(os.FileInfo) error { return nil }
	e.s.ReadRevoked = nil
	if !e.s.HasOwner() {
		t.Fatal("no list: the owner should stand")
	}
	os.WriteFile(appstate.RevokedPath(), []byte(fmt.Sprintf(`{"version":1,"uid":%d,"revoked":[]}`, os.Getuid()+1)), 0o644)
	if e.s.HasOwner() {
		t.Fatal("a list for another uid was ignored")
	}
	var v struct {
		Code string `json:"revocationsCode"`
		Err  string `json:"revocationsError"`
	}
	s25.json("GET", "/api/passkeys", "", nil, &v)
	if v.Code != "revoked-list-invalid" || !strings.Contains(v.Err, "uid") {
		t.Fatalf("passkeys = %+v", v)
	}
}
