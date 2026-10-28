package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The E2E build is a different binary: the normal one never contains its fake OS
// authentication, and the E2E one refuses to run against a real HOME.
func TestE2EBuildIsSeparate(t *testing.T) {
	if testing.Short() {
		t.Skip("builds two binaries")
	}
	dir := t.TempDir()
	normal, e2e := filepath.Join(dir, "truehand"), filepath.Join(dir, "truehand-e2e")
	for _, b := range []struct{ out, tags string }{{normal, ""}, {e2e, "truehand_e2e"}} {
		args := []string{"build", "-o", b.out}
		if b.tags != "" {
			args = append(args, "-tags", b.tags)
		}
		if out, err := exec.Command("go", append(args, "github.com/spaghetticoders/truehand/cmd/truehand")...).CombinedOutput(); err != nil {
			t.Fatalf("go build %s: %v\n%s", b.tags, err, out)
		}
	}
	syms := func(bin string) string {
		out, err := exec.Command("go", "tool", "nm", bin).Output()
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	if strings.Contains(syms(normal), "e2eAuthenticate") {
		t.Fatal("the normal binary contains the E2E fake authentication")
	}
	if !strings.Contains(syms(e2e), "e2eAuthenticate") {
		t.Fatal("the symbol check would not notice the fake")
	}
	run := func(bin, home string) (string, error) {
		cmd := exec.Command(bin, "version")
		cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	realHome, _ := os.UserHomeDir()
	if out, err := run(e2e, realHome); err == nil || !strings.Contains(out, "temporary directory") {
		t.Fatalf("E2E build with a real HOME: %v %s", err, out)
	}
	tmpHome, _ := os.MkdirTemp("/tmp", "the2e")
	defer os.RemoveAll(tmpHome)
	if out, err := run(e2e, tmpHome); err != nil || !strings.Contains(out, "E2E BUILD — NOT FOR USE") {
		t.Fatalf("E2E build with a temporary HOME: %v %s", err, out)
	}
	realRepo, _ := os.Getwd()
	// If the refusal regresses, serve starts and stays up: the deadline turns that into a
	// readable failure instead of the test binary's timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, e2e, "serve", "--app", "--addr", "127.0.0.1:0", "--repo", realRepo)
	cmd.Env = []string{"HOME=" + tmpHome, "PATH=/usr/bin:/bin"}
	cmd.Stdin = strings.NewReader(`{"token":"` + strings.Repeat("t", 40) + `","shell_secret":"` + strings.Repeat("s", 40) + `"}` + "\n")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("the E2E build served a real repo instead of refusing it:\n%s", out)
	}
	if err == nil || !strings.Contains(string(out), "registers only repos under a temporary directory") {
		t.Fatalf("E2E build with a real repo: %v %s", err, out)
	}
	if out, err := run(normal, realHome); err != nil || strings.Contains(out, "E2E") {
		t.Fatalf("normal build: %v %s", err, out)
	}
}
