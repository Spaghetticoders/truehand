package hands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/core"
)

// A task judged at a stack point is judged on that commit: the next task's uncommitted work in
// the same files must not close the chain under it (Wovio COACH-3, 2026-11-09).
func TestStackPointIgnoresTheNextTasksWorkInProgress(t *testing.T) {
	cfg := stackConfig + "  delivered:\n    - check: evidence_matches_head\n"
	dir := filepath.Join(t.TempDir(), "demo")
	os.MkdirAll(dir, 0o755)
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "h@x.y"}, {"config", "user.name", "H"}, {"config", "core.hooksPath", ".git/hooks"}} {
		sgit(t, dir, a...)
	}
	swrite(t, dir, ".truehand/config.yaml", cfg)
	swrite(t, dir, "base.txt", "base\n")
	commitAll(t, dir, "chore: truehand")
	sgit(t, dir, "checkout", "-q", "-b", "feat/pocket-coach")
	ws, err := core.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	c1, err := ws.NewTask("COACH-1", "first", "")
	if err != nil {
		t.Fatal(err)
	}
	swrite(t, dir, "a.txt", "coach one\n")
	commitAll(t, dir, "feat: coach one")
	review(t, ws, c1)
	c1.State = board.Delivered
	ws.Store.Save(c1)
	tip := commitAll(t, dir, "chore: coach one delivered")

	ws = reopen(t, dir)
	c2, err := ws.NewTask("COACH-2", "second", "")
	if err != nil {
		t.Fatal(err)
	}
	if c2.StackedOn != "COACH-1" || c2.Base != tip {
		t.Fatalf("COACH-2 not stacked on COACH-1 at %s: %+v", tip, c2)
	}
	c2.State = board.Building
	ws.Store.Save(c2)
	swrite(t, dir, "a.txt", "coach one, then two\n")

	ws = reopen(t, dir)
	c2, _ = ws.Store.Load("COACH-2")
	if b, err := ws.BaseOf(c2); err != nil || b != tip {
		t.Fatalf("COACH-2's base with its own work uncommitted: %q, %v", b, err)
	}
	c1, _ = ws.Store.Load("COACH-1")
	if res, err := ws.Check(c1, board.Delivered); err != nil || !res.Pass {
		t.Fatalf("COACH-1 judged at the stack point: %+v %v", res, err)
	}
}
