package core

import (
	"path/filepath"
	"testing"

	"github.com/spaghetticoders/truehand/internal/board"

	"github.com/spaghetticoders/truehand/internal/config"
	"github.com/spaghetticoders/truehand/internal/glob"
)

// The files proposed for a wider scope are paths, read by people as paths. A file whose name
// is a glob ("**") must not become, once accepted, a pattern that owns every file.
func TestReviewProposedScopeIsLiteralPaths(t *testing.T) {
	dir := bareRepo(t)
	write(t, config.Path(dir), scopeConfig)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "chore: truehand")
	git(t, dir, "checkout", "-q", "-b", "feat")
	ws, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ws.NewTask("T-1", "x", "", "lib/**")
	write(t, filepath.Join(dir, "lib/a.go"), "package lib\n")
	write(t, filepath.Join(dir, "docs/**"), "looks like a note\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "feat: a")
	ws = reopen(t, dir)
	plan, err := ws.PlanScope("T-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.WidenScope("T-1", plan.Add, "human:h@x.y", nil); err != nil {
		t.Fatal(err)
	}
	task, _ := ws.Store.Load("T-1")
	for _, other := range []string{"docs/secret/deploy.yml", "docs/x"} {
		if glob.MatchAny(task.Scope, other) {
			t.Errorf("accepting the proposed %v gave T-1 %s, a file it never changed", plan.Add, other)
		}
	}
}

// A delivered task is judged on its own commits: later work on the branch by another task
// (not stacked on it) is not "outside its scope".
func TestReviewLaterCommitsAreNotADeliveredTasksOutside(t *testing.T) {
	dir := bareRepo(t)
	write(t, config.Path(dir), scopeConfig)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "chore: truehand")
	git(t, dir, "checkout", "-q", "-b", "feat")
	ws, _ := Open(dir)
	ws.NewTask("T-1", "x", "", "lib/**")
	write(t, filepath.Join(dir, "lib/a.go"), "package lib\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "feat: a")
	task, _ := ws.Store.Load("T-1")
	task.State = board.Delivered
	ws.Store.Save(task)
	ws.NewTask("T-2", "y", "")
	write(t, filepath.Join(dir, "web/b.ts"), "x\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "feat: b")
	ws = reopen(t, dir)
	if ok, msg := inScope(t, ws, "T-1"); !ok {
		t.Fatalf("T-1, delivered, fails in_scope because of T-2's later commit: %s", msg)
	}
}
