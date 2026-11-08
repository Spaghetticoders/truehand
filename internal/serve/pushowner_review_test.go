package serve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An owner confirms the push of head A from the phone; the base then moves to B (another
// config commit, or a code commit). The confirmation never pushes B, and never a code commit.
func TestReviewOwnerPushIsBoundToTheHeadShown(t *testing.T) {
	e := newEnv(t, false)
	a := e.headless()
	s25 := e.phone(false)
	e.bootstrap(a, s25)
	cfg := "version: 0\nbase: origin/main\ngates:\n  approved:\n    - check: plan_valid\n    - check: human_approval\n"
	os.WriteFile(filepath.Join(e.root, ".truehand/config.yaml"), []byte(cfg), 0o644)
	git(t, e.root, "commit", "-q", "-am", "chore: remote base")
	tip := strings.TrimSpace(git(t, e.root, "rev-parse", "HEAD"))
	git(t, e.root, "update-ref", "refs/remotes/origin/main", tip)
	os.WriteFile(filepath.Join(e.root, ".truehand/config.yaml"), []byte(cfg+"# v2\n"), 0o644)
	git(t, e.root, "commit", "-q", "-am", "chore: config v2")
	os.WriteFile(filepath.Join(e.root, ".truehand/config.yaml"), []byte(cfg+"# v2b\n"), 0o644)
	git(t, e.root, "commit", "-q", "-am", "chore: config v2b")
	var pushed []string
	e.s.Run = func(ctx context.Context, d string, env []string, name string, args ...string) ([]byte, error) {
		switch {
		case name == "git" && args[0] == "push":
			pushed = append(pushed, strings.Join(args, " "))
			return nil, nil
		case name == "git" && args[0] == "ls-remote":
			return []byte(tip + "\trefs/heads/main\n"), nil
		}
		return runCmd(ctx, d, env, name, args...)
	}
	var prev struct {
		Push pushView `json:"push"`
	}
	if c := s25.json("GET", "/api/projects/"+e.pid+"/push-base", "", nil, &prev); c != 200 || !prev.Push.OnlyConfig {
		t.Fatalf("preview = %d %+v", c, prev)
	}
	shownA := prev.Push.Head
	path := "/api/projects/" + e.pid + "/push-base"
	shownFrom := prev.Push.From
	body := `{"from":"` + shownFrom + `","head":"` + shownA + `"}`

	// The base moves after the person looked: another config commit.
	os.WriteFile(filepath.Join(e.root, ".truehand/config.yaml"), []byte(cfg+"# v3\n"), 0o644)
	git(t, e.root, "commit", "-q", "-am", "chore: config v3")
	if c := s25.asOwner("POST", path, body, nil); c != 409 || len(pushed) != 0 {
		t.Fatalf("owner confirmed head %.7s, base is now another commit: %d, pushes %v", shownA, c, pushed)
	}
	// Back to A, plus a code commit on top, shown honestly: refused on the owner path too.
	os.WriteFile(filepath.Join(e.root, "README.md"), []byte("x"), 0o644)
	git(t, e.root, "add", "README.md")
	git(t, e.root, "commit", "-q", "-m", "docs: readme")
	s25.json("GET", path, "", nil, &prev)
	if c := s25.asOwner("POST", path, `{"from":"`+prev.Push.From+`","head":"`+prev.Push.Head+`"}`, nil); c != 409 || len(pushed) != 0 {
		t.Fatalf("owner push with a code commit: %d, pushes %v", c, pushed)
	}
	// The owner's signature covers the body: a confirmation for A does not carry another head.
	git(t, e.root, "reset", "-q", "--hard", "HEAD~2")
	_, h := s25.signed("owner", map[string]string{"method": "POST", "path": path, "bodySha256": bodySHA(body)})
	if c := s25.json("POST", path, `{"from":"`+shownFrom+`","head":"`+tip+`"}`, map[string]string{ownerHeader: h}, nil); c == 200 || len(pushed) != 0 {
		t.Fatalf("owner signature for head A used with another body: %d", c)
	}
	// origin moves after the preview: the same confirmation no longer fits.
	moved := strings.TrimSpace(git(t, e.root, "rev-parse", "HEAD~1"))
	realTip := tip
	tip = moved
	if c := s25.asOwner("POST", path, body, nil); c != 409 || len(pushed) != 0 {
		t.Fatalf("owner push after origin moved = %d, pushes %v", c, pushed)
	}
	tip = realTip
	if c := s25.asOwner("POST", path, body, nil); c != 200 || len(pushed) != 1 {
		t.Fatalf("good owner push = %d, pushes %v", c, pushed)
	}
	if want := "push --force-with-lease=refs/heads/main:" + tip + " origin " + shownA + ":refs/heads/main"; pushed[0] != want {
		t.Fatalf("pushed %q, want %q", pushed[0], want)
	}
}
