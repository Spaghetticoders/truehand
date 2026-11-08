package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, dir, file, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	sh(t, dir, "add", file)
	sh(t, dir, "commit", "-q", "-m", file)
	return sh(t, dir, "rev-parse", "HEAD")
}

func newRepo(t *testing.T) (string, *Repo) {
	dir := t.TempDir()
	sh(t, dir, "init", "-q", "-b", "main")
	commit(t, dir, "a", "1\n")
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir, r
}

// A replace ref shows another commit under an id to plain git; gitx reads the real one.
func TestRunIgnoresReplaceRefs(t *testing.T) {
	dir, r := newRepo(t)
	base := sh(t, dir, "rev-parse", "HEAD")
	real := commit(t, dir, "evil", "x\n")
	sh(t, dir, "reset", "-q", "--hard", base)
	clean := commit(t, dir, "clean", "y\n")
	sh(t, dir, "replace", real, clean)
	if plain := sh(t, dir, "diff", "--name-only", base, real); plain != "clean" {
		t.Fatalf("setup: replace not in effect: %q", plain)
	}
	t.Setenv("GIT_NO_REPLACE_OBJECTS", "1")
	os.Unsetenv("GIT_NO_REPLACE_OBJECTS")
	out, err := r.Git("diff", "--name-only", base, real)
	if err != nil || strings.TrimSpace(out) != "evil" {
		t.Fatalf("gitx saw the replacement: %q %v", out, err)
	}
	if b, ok, err := r.BlobAt(real, "evil"); err != nil || !ok || string(b) != "x\n" {
		t.Fatalf("BlobAt through replace: %q %v %v", b, ok, err)
	}
}

// Grafts invent parents; gitx ignores them, even one named through the environment.
func TestRunIgnoresGrafts(t *testing.T) {
	dir, r := newRepo(t)
	first := sh(t, dir, "rev-parse", "HEAD")
	second := commit(t, dir, "b", "2\n")
	graft := filepath.Join(t.TempDir(), "grafts")
	os.WriteFile(graft, []byte(second+"\n"), 0o644)
	t.Setenv("GIT_GRAFT_FILE", graft)
	if _, err := r.Git("merge-base", "--is-ancestor", first, second); err != nil {
		t.Fatal("a graft cut the history gitx reads")
	}
	if r.HistoryRewritten() != "" {
		t.Fatal(r.HistoryRewritten())
	}
	gd := sh(t, dir, "rev-parse", "--git-common-dir")
	os.MkdirAll(filepath.Join(dir, gd, "info"), 0o755)
	os.WriteFile(filepath.Join(dir, gd, "info", "grafts"), []byte(second+"\n"), 0o644)
	if !strings.Contains(r.HistoryRewritten(), "grafts") {
		t.Fatal("info/grafts not reported")
	}
}

func TestHistoryRewrittenShallow(t *testing.T) {
	dir, _ := newRepo(t)
	commit(t, dir, "b", "2\n")
	clone := filepath.Join(t.TempDir(), "c")
	sh(t, dir, "clone", "-q", "--depth", "1", "file://"+dir, clone)
	r, err := Open(clone)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.HistoryRewritten(), "shallow") {
		t.Fatalf("shallow clone not reported: %q", r.HistoryRewritten())
	}
}

// A failing command's error carries git's message, not the hint about grafts that
// GIT_GRAFT_FILE triggers.
func TestErrorsWithoutGraftHint(t *testing.T) {
	_, r := newRepo(t)
	_, err := r.Git("merge-base", "HEAD", "no-such-ref")
	if err == nil {
		t.Fatal("merge-base with a missing ref succeeded")
	}
	if strings.Contains(strings.ToLower(err.Error()), "graft") {
		t.Fatalf("graft hint in the error: %v", err)
	}
}
