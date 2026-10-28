package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Tests may run inside an agent session; identity must come from TRUEHAND_ACTOR or git here.
func TestMain(m *testing.M) {
	// A child the peer tests inspect: a plain binary, so macOS lets ps read its environment.
	if os.Getenv("TRUEHAND_TEST_IDLE") == "1" {
		os.Stdout.WriteString("ready\n")
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	// Tests may run inside an agent the app launched; they must not count as one.
	os.Unsetenv("TRUEHAND_SESSION")
	AgentDetector = func() int { return 0 }
	os.Unsetenv("TRUEHAND_ACTOR")
	os.Exit(m.Run())
}

const testConfig = "version: 0\nbase: main\ngates:\n  approved:\n    - check: plan_valid\n    - check: human_approval\n"

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// bareRepo is a git repo on main with one commit and no Truehand config.
func bareRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "config", "user.email", "h@x.y")
	git(t, dir, "config", "user.name", "Human Name")
	git(t, dir, "config", "core.hooksPath", ".git/hooks")
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "chore: x")
	return dir
}

// repo is a git repo on main with .truehand/config.yaml committed.
func repo(t *testing.T) (string, *Workspace) {
	t.Helper()
	dir := bareRepo(t)
	write(t, filepath.Join(dir, ".truehand/config.yaml"), testConfig)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "chore: truehand")
	ws, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir, ws
}
