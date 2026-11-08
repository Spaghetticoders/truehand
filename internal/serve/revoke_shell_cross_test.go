package serve

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spaghetticoders/truehand/internal/appstate"
	"github.com/spaghetticoders/truehand/internal/appstate/webauthntest"
	"github.com/spaghetticoders/truehand/internal/sign"
)

// TestShellRevokeCross: the Rust shell builds its durable revocation from this server's
// GET /api/passkeys, sends it, and the owner it names stops being one, on its id and its key.
func TestShellRevokeCross(t *testing.T) {
	bin := os.Getenv("TRUEHAND_SHELL_TEST")
	if bin == "" {
		t.Skip("TRUEHAND_SHELL_TEST not set: cd shell/src-tauri && cargo test --no-run, then set it to the truehand_shell-* test binary it prints")
	}
	e := newEnv(t, false)
	e.s.SetHosts([]string{tsHost})
	e.s.ReadAnchor = func() (*appstate.Anchor, error) { return nil, nil }
	e.s.ReadRevoked = func() ([]appstate.Revocation, error) { return nil, nil }
	var code struct {
		Code string `json:"code"`
	}
	e.json("POST", "/api/passkeys/enroll-code", `{"owner":true}`, asShell, &code)
	p := e.phone(false)
	if c, v := p.register(code.Code); c != 200 || v.Pending != "mac" {
		t.Fatalf("register = %d %+v", c, v)
	}
	mac := mustKey(t)
	e.s.MacKey = &mac.PublicKey
	id := p.auth.CredentialID()
	var ai struct {
		Intent sign.ServerIntent `json:"intent"`
	}
	e.json("GET", "/api/passkeys/"+id+"/anchor-intent", "", asShell, &ai)
	b, _ := json.Marshal(map[string]any{"signed": webauthntest.SignES256(mac, ai.Intent)})
	if c := e.code("POST", "/api/passkeys/"+id+"/anchor", string(b), asShell); c != 204 {
		t.Fatalf("anchor = %d", c)
	}
	// Until the shell has given this process its list, nobody is an owner.
	if e.s.HasOwner() {
		t.Fatal("owner before the Mac app sent its revocations")
	}
	// What serve --app does with the list the shell wrote on its stdin.
	e.s.SetMacRevoked([]appstate.Revocation{})
	if !e.s.HasOwner() {
		t.Fatal("no owner once the shell's empty list arrived")
	}

	dir := t.TempDir()
	resp, list := e.req("GET", "/api/passkeys", "", asToken)
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	os.WriteFile(filepath.Join(dir, "passkeys.json"), list, 0o600)
	os.WriteFile(filepath.Join(dir, "id"), []byte(id), 0o600)
	cmd := exec.Command(bin, "revoke::tests::builds_the_revoked_body_from_a_real_server", "--exact")
	cmd.Env = append(os.Environ(), "TRUEHAND_REVOKE_SERVER="+dir)
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "1 passed") {
		t.Fatalf("rust: %v\n%s", err, out)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "revoked.json"))
	if d, _ := os.ReadFile(filepath.Join(dir, "delete")); string(d) != "true" {
		t.Fatalf("delete = %s", d)
	}
	ps, _ := e.s.Store.Passkeys()
	pk, _ := ps.Get(id)
	var sent struct {
		Revoked []appstate.Revocation `json:"revoked"`
	}
	if err := json.Unmarshal(body, &sent); err != nil || len(sent.Revoked) != 1 {
		t.Fatalf("body %s: %v", body, err)
	}
	want := appstate.RevocationOf(pk, sent.Revoked[0].Time)
	if sent.Revoked[0] != want || sent.Revoked[0].Time.IsZero() {
		t.Fatalf("rust sent %+v, server would write %+v", sent.Revoked[0], want)
	}
	// Only the shell, with the secret it started the server with: a page or an agent with the
	// token alone is refused, and so is a wrong secret.
	if c := e.code("POST", "/api/shell/revoked", `{"revoked":[]}`, asToken); c != http.StatusForbidden {
		t.Fatalf("revoked with the token alone = %d", c)
	}
	if c := e.code("POST", "/api/shell/revoked", `{"revoked":[]}`, anon); c != http.StatusForbidden && c != http.StatusUnauthorized {
		t.Fatalf("revoked without credentials = %d", c)
	}
	wrong, _ := http.NewRequest("POST", e.srv.URL+"/api/shell/revoked", strings.NewReader(`{"revoked":[]}`))
	wrong.Header.Set("Content-Type", jsonType)
	wrong.Header.Set("Authorization", "Bearer "+testToken)
	wrong.Header.Set(shellHeader, testShell+"x")
	if r, err := http.DefaultClient.Do(wrong); err != nil || r.StatusCode != http.StatusForbidden {
		t.Fatalf("revoked with a wrong shell secret = %v %v", r, err)
	}
	if !e.s.HasOwner() {
		t.Fatal("a refused list changed the owners")
	}
	if c := e.code("POST", "/api/shell/revoked", string(body), asShell); c != 204 {
		t.Fatalf("revoked = %d", c)
	}
	if e.s.HasOwner() {
		t.Fatal("owner still valid after the Mac revoked it")
	}
	// Matched by key too: the same key under another id stays revoked.
	only := strings.Replace(string(body), `"credential":"`+id+`"`, `"credential":"someoneelse"`, 1)
	if c := e.code("POST", "/api/shell/revoked", only, asShell); c != 204 || e.s.HasOwner() {
		t.Fatalf("revoked by key = %d, owner %v", c, e.s.HasOwner())
	}
	var pv struct {
		Revocations []revocationView `json:"revocations"`
	}
	e.json("GET", "/api/passkeys", "", asToken, &pv)
	if len(pv.Revocations) != 1 || pv.Revocations[0].Revoked != "durable" {
		t.Fatalf("revocations = %+v", pv.Revocations)
	}
	// The whole list replaces the server's: an empty one lifts the revocation.
	if c := e.code("POST", "/api/shell/revoked", `{"revoked":[]}`, asShell); c != 204 || !e.s.HasOwner() {
		t.Fatalf("empty list = %d, owner %v", c, e.s.HasOwner())
	}
}
