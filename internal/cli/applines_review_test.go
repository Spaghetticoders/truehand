package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// The line the Mac app writes on the server's stdin (shell/src-tauri/src/server.rs app_line):
// token, shell_secret, mac_key and revoked. The server must read all of it, also with a long
// revocation list.
func TestReviewAppLineFromTheShell(t *testing.T) {
	pem := "-----BEGIN PUBLIC KEY-----\nMFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEpJ3sH2GH3BQnCOHvGg49/mkpronY\ncYHChEO5wQRFzGgKTrscdZv6NWjsc1mc1F3Vi+01zmfqX/6+MRIYuwI4dw==\n-----END PUBLIC KEY-----\n"
	var revoked []map[string]string
	for i := 0; i < 40; i++ {
		revoked = append(revoked, map[string]string{"credential": strings.Repeat("A", 22) + string(rune('a'+i%26)), "public_key_sha256": strings.Repeat("ab", 32), "label": "Galaxy S25 Ultra", "time": "2026-11-08T10:00:00Z"})
	}
	b, _ := json.Marshal(map[string]any{"token": strings.Repeat("t", 43), "shell_secret": strings.Repeat("s", 43), "mac_key": pem, "revoked": revoked})
	cr, err := readAppCredentials(strings.NewReader(string(b) + "\n"))
	if err != nil {
		t.Fatalf("the shell's line with %d revocations (%d bytes) refused: %v", len(revoked), len(b), err)
	}
	if cr.ApproverPublicKey == "" {
		t.Error("the Mac key the shell sends as mac_key is not read")
	}
	out, _ := json.Marshal(cr)
	if !strings.Contains(string(out), "public_key_sha256") {
		t.Error("the revocations the shell sends on stdin are not read")
	}
}
