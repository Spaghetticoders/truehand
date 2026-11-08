package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spaghetticoders/truehand/internal/appstate"
)

// TestShellAppLineCross reads, as serve --app does, the line the Rust shell writes on its
// stdin (TRUEHAND_SHELL_TEST, the cargo test binary).
func TestShellAppLineCross(t *testing.T) {
	bin := os.Getenv("TRUEHAND_SHELL_TEST")
	if bin == "" {
		t.Skip("TRUEHAND_SHELL_TEST not set: cd shell/src-tauri && cargo test --no-run, then set it to the truehand_shell-* test binary it prints")
	}
	run := func(empty bool) appCredentials {
		f := filepath.Join(t.TempDir(), "line")
		cmd := exec.Command(bin, "server::tests::writes_the_app_line_for_the_go_reader", "--exact")
		cmd.Env = append(os.Environ(), "TRUEHAND_APP_LINE="+f)
		if empty {
			cmd.Env = append(cmd.Env, "TRUEHAND_APP_LINE_EMPTY=1")
		}
		if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "1 passed") {
			t.Fatalf("rust: %v\n%s", err, out)
		}
		b, _ := os.ReadFile(f)
		cr, err := readAppCredentials(strings.NewReader(string(b) + "trailing junk is not read\n"))
		if err != nil {
			t.Fatalf("readAppCredentials(%s): %v", b, err)
		}
		return cr
	}
	cr := run(false)
	want := appstate.Revocation{Credential: "cred_1", PublicKeySHA256: strings.Repeat("a", 64), Key: "0123456789ab", Label: "S25 «x»", Time: time.Date(2026, 11, 8, 1, 0, 0, 0, time.UTC)}
	if len(cr.Revoked) != 1 || cr.Revoked[0] != want {
		t.Fatalf("revoked = %+v", cr.Revoked)
	}
	if !strings.HasPrefix(cr.ApproverPublicKey, "-----BEGIN PUBLIC KEY-----\n") || len(cr.Token) < 32 || len(cr.ShellSecret) < 32 {
		t.Fatalf("credentials = %+v", cr)
	}
	cr = run(true)
	if cr.Revoked == nil || len(cr.Revoked) != 0 || cr.ApproverPublicKey != "" {
		t.Fatalf("empty = %+v", cr)
	}
}
