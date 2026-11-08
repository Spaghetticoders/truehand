package hands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/ci"
)

// COACH-1 is judged at the stack point; a later commit changes a.txt again, outside the scope
// of COACH-2 stacked on it. Nobody verified a.txt as it is at HEAD: CI must not count it as
// covered by COACH-1's fingerprint at the point.
func TestReviewLaterEditOfAStackedFileIsNotCovered(t *testing.T) {
	dir, ws, _ := stackRepo(t)
	c2, err := ws.NewTask("COACH-2", "second", "", "b.txt")
	if err != nil || c2.StackedOn != "COACH-1" {
		t.Fatalf("COACH-2 not stacked: %+v %v", c2, err)
	}
	swrite(t, dir, "a.txt", "changed after COACH-1 was reviewed, by nobody's task\n")
	swrite(t, dir, "b.txt", "coach two\n")
	commitAll(t, dir, "feat: coach two")
	ws = reopen(t, dir)
	c2, _ = ws.Store.Load("COACH-2")
	review(t, ws, c2)
	c2.State = board.Delivered
	ws.Store.Save(c2)
	c1, _ := ws.Store.Load("COACH-1")
	c1.State = board.Delivered
	ws.Store.Save(c1)
	commitAll(t, dir, "chore: delivered")
	r, err := ci.Verify(reopen(t, dir), "main")
	if err != nil {
		t.Fatal(err)
	}
	if r.Pass {
		t.Fatalf("CI passed with a.txt changed after the point that judged COACH-1, in no task's scope: covers %+v", r.Tasks)
	}
}

// The stack point named in a forged task.yaml must not make a task pass that its working
// tree no longer matches: a forged child stacked on COACH-1 at its review commit, while
// a.txt changed afterwards.
func TestReviewForgedChildDoesNotFreezeItsParent(t *testing.T) {
	dir, ws, tip := stackRepo(t)
	swrite(t, dir, "a.txt", "edited after review\n")
	commitAll(t, dir, "feat: edit after review")
	ws.Store.Create(&board.Task{ID: "FAKE", Title: "x", State: board.Planned, Base: tip, BaseRef: "main", StackedOn: "COACH-1", Scope: []string{"nothing"}})
	ws = reopen(t, dir)
	c1, _ := ws.Store.Load("COACH-1")
	c1.State = board.Delivered
	ws.Store.Save(c1)
	commitAll(t, dir, "chore: delivered")
	r, err := ci.Verify(reopen(t, dir), "main")
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range r.Tasks {
		if tr.ID == "COACH-1" && tr.Pass {
			for _, f := range tr.Covers {
				if f == "a.txt" {
					t.Fatalf("a forged child froze COACH-1 at its review commit: CI counts a.txt (edited later) as covered by it")
				}
			}
		}
	}
}

// Without a scope, the hand ran with lib/a_fix.txt in the working tree (the file that made it
// pass), which never got committed. At the point, the evidence describes content the point
// does not hold: it must be stale, as it is on the working tree without a stack.
func TestReviewUncommittedFileTheHandSawIsNotIgnoredAtThePoint(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	os.MkdirAll(dir, 0o755)
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "h@x.y"}, {"config", "user.name", "H"}, {"config", "core.hooksPath", ".git/hooks"}} {
		sgit(t, dir, a...)
	}
	swrite(t, dir, ".truehand/config.yaml", stackConfig)
	commitAll(t, dir, "chore: truehand")
	sgit(t, dir, "checkout", "-q", "-b", "feat")
	ws := reopen(t, dir)
	c1, _ := ws.NewTask("COACH-1", "first", "")
	swrite(t, dir, "lib/a.txt", "one\n")
	swrite(t, dir, "lib/a_fix.txt", "what made the hand pass\n")
	review(t, ws, c1)
	os.Remove(filepath.Join(dir, "lib/a_fix.txt"))
	c1.State = board.Reviewed
	ws.Store.Save(c1)
	sgit(t, dir, "add", "lib/a.txt", ".truehand")
	sgit(t, dir, "commit", "-q", "-m", "feat: one")
	main := sgit(t, dir, "rev-parse", "main")
	ws.Store.Create(&board.Task{ID: "COACH-2", Title: "second", State: board.Building, Base: main, BaseRef: "main"})
	swrite(t, dir, "lib/b.txt", "two\n")
	sgit(t, dir, "add", "lib/b.txt", ".truehand")
	sgit(t, dir, "commit", "-q", "-m", "feat: two")
	ws = reopen(t, dir)
	if point, pred, err := ws.RestackPoint("COACH-2"); err == nil {
		t.Fatalf("COACH-2 restacks on %s at %.12s: COACH-1's evidence counted as fresh at a point without the file its hand needed", pred.ID, point)
	}
}

// Narrowing a reviewed task's scope after its hand ran drops, at the point, an input the hand
// saw inside the old scope. Without a stack the same narrowing leaves the evidence stale.
func TestReviewNarrowedScopeDoesNotHideAnInputAtThePoint(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	os.MkdirAll(dir, 0o755)
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "h@x.y"}, {"config", "user.name", "H"}, {"config", "core.hooksPath", ".git/hooks"}} {
		sgit(t, dir, a...)
	}
	swrite(t, dir, ".truehand/config.yaml", stackConfig)
	commitAll(t, dir, "chore: truehand")
	sgit(t, dir, "checkout", "-q", "-b", "feat")
	ws := reopen(t, dir)
	c1, _ := ws.NewTask("COACH-1", "first", "", "lib/**", "fix/**")
	swrite(t, dir, "lib/a.txt", "one\n")
	swrite(t, dir, "fix/helper.txt", "what made the hand pass\n")
	review(t, ws, c1)
	os.RemoveAll(filepath.Join(dir, "fix"))
	c1.State = board.Reviewed
	c1.Scope = []string{"lib/**"}
	ws.Store.Save(c1)
	sgit(t, dir, "add", "lib/a.txt", ".truehand")
	sgit(t, dir, "commit", "-q", "-m", "feat: one")
	main := sgit(t, dir, "rev-parse", "main")
	ws.Store.Create(&board.Task{ID: "COACH-2", Title: "second", State: board.Building, Base: main, BaseRef: "main"})
	swrite(t, dir, "lib/b.txt", "two\n")
	sgit(t, dir, "add", "lib/b.txt", ".truehand")
	sgit(t, dir, "commit", "-q", "-m", "feat: two")
	ws = reopen(t, dir)
	if point, pred, err := ws.RestackPoint("COACH-2"); err == nil {
		t.Fatalf("COACH-2 restacks on %s at %.12s: narrowing COACH-1's scope after its hand hid fix/helper.txt", pred.ID, point)
	}
}
