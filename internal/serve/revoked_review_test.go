package serve

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/spaghetticoders/truehand/internal/appstate"
	"github.com/spaghetticoders/truehand/internal/appstate/webauthntest"
	"github.com/spaghetticoders/truehand/internal/sign"
)

// The Mac app passes its key on stdin when the server starts, and its Keychain revocations
// only later, with a request. A passkey revoked in the Keychain must not be an owner in
// between (a restarted server, the shell not yet heard from).
func TestReviewMacRevokedOwnerBeforeTheShellSpeaks(t *testing.T) {
	e := newEnv(t, false)
	e.s.SetHosts([]string{tsHost})
	e.s.ReadAnchor = func() (*appstate.Anchor, error) { return nil, nil }
	e.s.ReadRevoked = func() ([]appstate.Revocation, error) { return nil, nil }
	var code struct {
		Code string `json:"code"`
	}
	e.json("POST", "/api/passkeys/enroll-code", `{"owner":true}`, asShell, &code)
	p := e.phone(false)
	if c, _ := p.register(code.Code); c != 200 {
		t.Fatalf("register = %d", c)
	}
	mac := mustKey(t)
	e.s.MacKey = &mac.PublicKey
	var ai struct {
		Intent sign.ServerIntent `json:"intent"`
	}
	e.json("GET", "/api/passkeys/"+p.auth.CredentialID()+"/anchor-intent", "", asShell, &ai)
	b, _ := json.Marshal(map[string]any{"signed": webauthntest.SignES256(mac, ai.Intent)})
	if c := e.code("POST", "/api/passkeys/"+p.auth.CredentialID()+"/anchor", string(b), asShell); c != http.StatusNoContent {
		t.Fatalf("anchor = %d", c)
	}
	ps, _ := e.s.Store.Passkeys()
	pk, _ := ps.Get(p.auth.CredentialID())
	rv, _ := json.Marshal(map[string]any{"revoked": []appstate.Revocation{appstate.RevocationOf(pk, timeNowForTest())}})
	if c := e.code("POST", "/api/shell/revoked", string(rv), asShell); c != http.StatusNoContent {
		t.Fatalf("shell revoked = %d", c)
	}
	if e.s.HasOwner() {
		t.Fatal("revoked in the Keychain, still an owner")
	}
	// A restart: same files, the Mac key from stdin, the Keychain list not sent yet.
	e.s.mu.Lock()
	e.s.macRevoked = nil
	e.s.mu.Unlock()
	if e.s.HasOwner() {
		t.Fatal("a passkey revoked in the Keychain is an owner until the Mac app sends its list again")
	}
}

func timeNowForTest() time.Time { return time.Now() }

// tailscale serve proxies with Go's ReverseProxy, which appends the client's address to any
// X-Forwarded-For the client sent: the first entry is the client's to choose, the last is
// the one tailscale saw. One client rotating the first entry is still one source.
func TestReviewEnrollLimitNotDodgedByForwardedFor(t *testing.T) {
	e := newEnv(t, false)
	e.headless()
	p := e.phone(false)
	try := func(i int) int {
		b, _ := json.Marshal(map[string]string{"code": "WRONG" + string(rune('A'+i)), "label": "x"})
		c, _ := p.do("POST", "/api/passkeys/register/options", string(b), map[string]string{"X-Forwarded-For": "10.0.0." + string(rune('1'+i%9)) + ", 100.64.0.7"})
		return c
	}
	for i := 0; i < pairMaxFails; i++ {
		try(i)
	}
	if c := try(pairMaxFails); c != http.StatusTooManyRequests {
		t.Fatalf("after %d wrong codes from one tailnet address, rotating the forwarded-for head: %d", pairMaxFails, c)
	}
}
