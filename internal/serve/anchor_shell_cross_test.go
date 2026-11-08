package serve

import (
	"crypto/ecdsa"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spaghetticoders/truehand/internal/appstate"
	"github.com/spaghetticoders/truehand/internal/sign"
)

// TestShellAnchorCross runs the Rust shell's check and signature on this server's real
// replies, then sends what it signed back. TRUEHAND_SHELL_TEST is the shell's cargo test
// binary (cd shell/src-tauri && cargo test --no-run prints its path).
func TestShellAnchorCross(t *testing.T) {
	bin := os.Getenv("TRUEHAND_SHELL_TEST")
	if bin == "" {
		t.Skip("TRUEHAND_SHELL_TEST not set: build the shell's tests (cd shell/src-tauri && cargo test --no-run) and point it at the binary")
	}
	e := newEnv(t, false)
	e.s.SetHosts([]string{tsHost})
	e.s.ReadAnchor = func() (*appstate.Anchor, error) { return nil, nil }
	var code struct {
		Code string `json:"code"`
	}
	e.json("POST", "/api/passkeys/enroll-code", `{"owner":true}`, asShell, &code)
	p := e.phone(false)
	if c, v := p.register(code.Code); c != 200 || v.Pending != "mac" {
		t.Fatalf("register = %d %+v", c, v)
	}
	if err := e.s.Store.UpdateState(func(st *appstate.State) error {
		st.Settings.Remote = appstate.Remote{Mode: "tailscale-serve", Addr: tsHost}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	id := p.auth.CredentialID()
	dir := t.TempDir()
	save := func(name, path string, c cred) {
		resp, b := e.req("GET", path, "", c)
		if resp.StatusCode != 200 {
			t.Fatalf("%s = %d %s", path, resp.StatusCode, b)
		}
		os.WriteFile(filepath.Join(dir, name), b, 0o600)
	}
	read := func(n string) []byte { b, _ := os.ReadFile(filepath.Join(dir, n)); return b }
	// One round: the server prepares, the shell checks and signs with a new software key.
	round := func() *ecdsa.PublicKey {
		save("anchor-intent.json", "/api/passkeys/"+id+"/anchor-intent", asShell)
		save("passkeys.json", "/api/passkeys", asToken)
		save("settings.json", "/api/settings", asToken)
		os.WriteFile(filepath.Join(dir, "id"), []byte(id), 0o600)
		var ai struct {
			Device string `json:"device"`
		}
		json.Unmarshal(read("anchor-intent.json"), &ai)
		os.Remove(filepath.Join(dir, "device"))
		if ai.Device != "" {
			os.WriteFile(filepath.Join(dir, "device"), []byte(ai.Device), 0o600)
		}
		cmd := exec.Command(bin, "anchor::tests::checks_and_signs_replies_from_a_real_server", "--exact")
		cmd.Env = append(os.Environ(), "TRUEHAND_ANCHOR_SERVER="+dir)
		if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "1 passed") {
			t.Fatalf("rust: %v\n%s", err, out)
		}
		mac, err := sign.ParseP256PublicKey(string(read("mac.pem")))
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Signed sign.SignedServer `json:"signed"`
		}
		json.Unmarshal(read("anchor.json"), &body)
		if err := body.Signed.VerifyES256(mac, tsHost); err != nil {
			t.Fatalf("VerifyES256: %v", err)
		}
		return mac
	}

	round()
	ps, _ := e.s.Store.Passkeys()
	pk, _ := ps.Get(id)
	if string(read("key")) != keyFingerprint(pk.PublicKey) || string(read("label")) != pk.Label {
		t.Fatalf("rust shows key %s label %q, server %s %q", read("key"), read("label"), keyFingerprint(pk.PublicKey), pk.Label)
	}
	// The server checks the grant with the Mac key it was given: none, no owner.
	if c := e.code("POST", "/api/passkeys/"+id+"/anchor", string(read("anchor.json")), asShell); c == http.StatusNoContent || e.s.HasOwner() {
		t.Fatalf("anchor accepted without a Mac key: %d", c)
	}
	e.s.MacKey = round()
	if c := e.code("POST", "/api/passkeys/"+id+"/anchor", string(read("anchor.json")), asShell); c != http.StatusNoContent {
		t.Fatalf("anchor = %d", c)
	}
	e.code("POST", "/api/shell/revoked", `{"revoked":[]}`, asShell)
	if !e.s.HasOwner() {
		t.Fatal("passkey anchored by the shell's signature is not an owner")
	}
	if c := e.code("POST", "/api/passkeys/"+id+"/anchor", string(read("anchor.json")), asShell); c != http.StatusForbidden {
		t.Fatalf("replayed anchor = %d", c)
	}
}
