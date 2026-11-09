package hands

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/ci"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/evidence"
)

const stackConfig = "version: 0\nbase: main\ncommands:\n  review: \"true\"\ngates:\n  verified:\n    - check: hand_passed\n      with: { hand: review }\n"

func sgit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func swrite(t *testing.T, dir, name, s string) {
	t.Helper()
	p := filepath.Join(dir, name)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitAll(t *testing.T, dir, msg string) string {
	sgit(t, dir, "add", "-A")
	sgit(t, dir, "commit", "-q", "-m", msg)
	return sgit(t, dir, "rev-parse", "HEAD")
}

// stackRepo: main with the config, a feature branch where COACH-1 changed a.txt, has passing
// evidence on it and is reviewed; everything committed.
func stackRepo(t *testing.T) (string, *core.Workspace, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "demo")
	os.MkdirAll(dir, 0o755)
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "h@x.y"}, {"config", "user.name", "H"}, {"config", "core.hooksPath", ".git/hooks"}} {
		sgit(t, dir, a...)
	}
	swrite(t, dir, ".truehand/config.yaml", stackConfig)
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
	c1.State = board.Reviewed
	ws.Store.Save(c1)
	tip := commitAll(t, dir, "chore: coach one reviewed")
	return dir, ws, tip
}

func review(t *testing.T, ws *core.Workspace, task *board.Task) {
	t.Helper()
	s, err := Begin(ws, task, "review", "")
	if err != nil {
		t.Fatal(err)
	}
	if r, err := s.Finish(evidence.Pass, "ok", ""); err != nil || r.Verdict != evidence.Pass {
		t.Fatalf("review: %v %v", r, err)
	}
}

func reopen(t *testing.T, dir string) *core.Workspace {
	ws, err := core.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

func TestStackedTaskStartsAfterTheReviewedOne(t *testing.T) {
	dir, ws, tip := stackRepo(t)
	c2, err := ws.NewTask("COACH-2", "second", "")
	if err != nil {
		t.Fatal(err)
	}
	if c2.StackedOn != "COACH-1" || c2.Base != tip {
		t.Fatalf("COACH-2 not stacked: base %s stacked_on %q (want %s on COACH-1)", c2.Base, c2.StackedOn, tip)
	}
	// COACH-2 changes a.txt too and adds b.txt; COACH-1 stays judged at the stack point.
	swrite(t, dir, "a.txt", "coach one, then two\n")
	swrite(t, dir, "b.txt", "coach two\n")
	commitAll(t, dir, "feat: coach two")
	ws = reopen(t, dir)
	c2, _ = ws.Store.Load("COACH-2")
	// Planned, COACH-2 does not close COACH-1 yet: only an approved task's stack does.
	c1, _ := ws.Store.Load("COACH-1")
	if tip, err := ws.TipOf(c1); err != nil || tip != "" {
		t.Fatalf("a planned task closed COACH-1 at %q: %v", tip, err)
	}
	c2.State = board.Approved
	ws.Store.Save(c2)
	ws = reopen(t, dir)
	fp, err := ws.Fingerprint(c2)
	if err != nil {
		t.Fatal(err)
	}
	if len(fp.Files) != 2 || fp.Files["a.txt"] == "" || fp.Files["b.txt"] == "" {
		t.Fatalf("COACH-2's change: %v", fp.Files)
	}
	c1, _ = ws.Store.Load("COACH-1")
	if res, err := ws.Check(c1, board.Verified); err != nil || !res.Pass {
		t.Fatalf("COACH-1 judged at the stack point: %+v %v", res, err)
	}
	if _, err := Begin(ws, c1, "review", ""); err == nil || !strings.Contains(err.Error(), "stacked") {
		t.Fatalf("a hand ran on a task closed by a stack: %v", err)
	}

	// The chain passes CI: COACH-1 covers a.txt at the point, COACH-2 a.txt and b.txt after it.
	review(t, ws, c2)
	c2.State = board.Delivered
	ws.Store.Save(c2)
	c1.State = board.Delivered
	ws.Store.Save(c1)
	commitAll(t, dir, "chore: delivered")
	r, err := ci.Verify(reopen(t, dir), "main")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Pass {
		t.Fatalf("stacked chain refused by CI: %v %v", r.Problems, r.Uncovered)
	}
}

// A commit between the base branch and HEAD that no reviewed task covers: no stack, the new
// task's change starts at the base branch as before.
func TestNoStackOverAnUncoveredCommit(t *testing.T) {
	dir, _, _ := stackRepo(t)
	swrite(t, dir, "stray.txt", "nobody's\n")
	commitAll(t, dir, "feat: stray")
	ws := reopen(t, dir)
	c2, err := ws.NewTask("COACH-2", "second", "")
	if err != nil {
		t.Fatal(err)
	}
	if c2.StackedOn != "" {
		t.Fatalf("stacked over an uncovered commit: %+v", c2)
	}
	if _, _, err := ws.RestackPoint("COACH-2"); err != nil {
		t.Fatalf("restack point: %v", err)
	}
}

// Restack finds the point that closes COACH-1 below COACH-2's own commits, and refuses where
// a commit in between belongs to nobody.
func TestRestack(t *testing.T) {
	dir, ws, tip := stackRepo(t)
	// COACH-2 made the old way: measured from the base branch, with commits of its own.
	main := sgit(t, dir, "rev-parse", "main")
	c2 := &board.Task{ID: "COACH-2", Title: "second", State: board.Building, Base: main, BaseRef: "main"}
	if err := ws.Store.Create(c2); err != nil {
		t.Fatal(err)
	}
	swrite(t, dir, "b.txt", "coach two\n")
	commitAll(t, dir, "feat: coach two")
	ws = reopen(t, dir)
	point, pred, err := ws.RestackPoint("COACH-2")
	if err != nil || point != tip || pred.ID != "COACH-1" {
		t.Fatalf("restack point %s %v %v (want %s on COACH-1)", point, pred, err, tip)
	}
	if _, err := ws.Restack("COACH-2", point, "COACH-1"); err != nil {
		t.Fatal(err)
	}
	ws = reopen(t, dir)
	c2, _ = ws.Store.Load("COACH-2")
	if base, err := ws.BaseOf(c2); err != nil || base != tip {
		t.Fatalf("base after restack %s %v", base, err)
	}

	// The same, with a commit nobody covers between COACH-1 and COACH-2's own.
	dir, ws, _ = stackRepo(t)
	swrite(t, dir, "stray.txt", "nobody's\n")
	commitAll(t, dir, "feat: stray")
	main = sgit(t, dir, "rev-parse", "main")
	ws.Store.Create(&board.Task{ID: "COACH-2", Title: "second", State: board.Building, Base: main, BaseRef: "main"})
	swrite(t, dir, "b.txt", "coach two\n")
	commitAll(t, dir, "feat: coach two")
	ws = reopen(t, dir)
	point, pred, err = ws.RestackPoint("COACH-2")
	if err != nil {
		t.Fatal(err)
	}
	// The only point that closes COACH-1 is below the stray commit: restacking there leaves
	// stray.txt in COACH-2's change, never hidden.
	if _, err := ws.Restack("COACH-2", point, pred.ID); err != nil {
		t.Fatal(err)
	}
	ws = reopen(t, dir)
	c2, _ = ws.Store.Load("COACH-2")
	fp, err := ws.Fingerprint(c2)
	if err != nil || fp.Files["stray.txt"] == "" || fp.Files["b.txt"] == "" {
		t.Fatalf("restack hid a commit: %v %v", fp.Files, err)
	}
}

// task.yaml edited by hand (or an agent) to stack where the chain does not hold: the task has
// no base, rather than one that hides commits.
func TestForgedStackRefused(t *testing.T) {
	dir, ws, _ := stackRepo(t)
	swrite(t, dir, "stray.txt", "nobody's\n")
	head := commitAll(t, dir, "feat: stray")
	ws = reopen(t, dir)
	ws.Store.Create(&board.Task{ID: "COACH-2", Title: "second", State: board.Planned, Base: head, BaseRef: "main", StackedOn: "COACH-1"})
	c2, _ := ws.Store.Load("COACH-2")
	if _, err := ws.BaseOf(c2); err == nil || !strings.Contains(err.Error(), "is stacked on COACH-1, but") {
		t.Fatalf("forged stack: %v", err)
	}
	if _, err := ws.Restack("COACH-2", head, "COACH-1"); err == nil {
		t.Fatal("restack accepted a point with an uncovered commit")
	}
	// Approved, the forged child still does not close COACH-1: its stack does not hold.
	c2.State = board.Approved
	ws.Store.Save(c2)
	ws = reopen(t, dir)
	c1, _ := ws.Store.Load("COACH-1")
	if tip, err := ws.TipOf(c1); err != nil || tip != "" {
		t.Fatalf("a stack that does not hold closed COACH-1 at %q: %v", tip, err)
	}
	// Stacked on a task that is not reviewed yet.
	c1, _ = ws.Store.Load("COACH-1")
	c1.State = board.Building
	ws.Store.Save(c1)
	c2.Base = sgit(t, dir, "rev-parse", "HEAD~1")
	ws.Store.Save(c2)
	ws = reopen(t, dir)
	c2, _ = ws.Store.Load("COACH-2")
	if _, err := ws.BaseOf(c2); err == nil || !strings.Contains(err.Error(), "reviewed or delivered") {
		t.Fatalf("stack on a building task: %v", err)
	}
}

// A reviewed task with a scope covers only its files: another file in its commits leaves the
// stack open, even though its own gates pass.
func TestNoStackOverFilesOutsideTheScope(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	os.MkdirAll(dir, 0o755)
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "h@x.y"}, {"config", "user.name", "H"}, {"config", "core.hooksPath", ".git/hooks"}} {
		sgit(t, dir, a...)
	}
	swrite(t, dir, ".truehand/config.yaml", stackConfig)
	commitAll(t, dir, "chore: truehand")
	sgit(t, dir, "checkout", "-q", "-b", "feat")
	ws := reopen(t, dir)
	c1, _ := ws.NewTask("COACH-1", "first", "", "a.txt")
	swrite(t, dir, "a.txt", "one\n")
	swrite(t, dir, "other.txt", "not COACH-1's\n")
	commitAll(t, dir, "feat: one")
	review(t, ws, c1)
	c1.State = board.Reviewed
	ws.Store.Save(c1)
	commitAll(t, dir, "chore: reviewed")
	ws = reopen(t, dir)
	if res, err := ws.Check(c1, board.Verified); err != nil || !res.Pass {
		t.Fatalf("setup: COACH-1 gates %+v %v", res, err)
	}
	c2, err := ws.NewTask("COACH-2", "second", "")
	if err != nil {
		t.Fatal(err)
	}
	if c2.StackedOn != "" {
		t.Fatal("stacked over other.txt, which COACH-1's scope does not cover")
	}
	if _, _, err := ws.RestackPoint("COACH-2"); err == nil || !strings.Contains(err.Error(), "other.txt") {
		t.Fatalf("restack point over other.txt: %v", err)
	}
}

func TestRestackByRecordsTheHuman(t *testing.T) {
	dir, ws, tip := stackRepo(t)
	main := sgit(t, dir, "rev-parse", "main")
	ws.Store.Create(&board.Task{ID: "COACH-2", Title: "second", State: board.Building, Base: main, BaseRef: "main"})
	ws = reopen(t, dir)
	if _, err := ws.RestackBy("COACH-2", tip, "COACH-1", "human:h@x.y", map[string]any{"via": "passkey", "base": "forged"}); err != nil {
		t.Fatal(err)
	}
	evs, _ := ws.Store.Events("COACH-2")
	last := evs[len(evs)-1]
	if last.Type != "restacked" || last.Actor != "human:h@x.y" || last.Detail["via"] != "passkey" || last.Detail["base"] != tip || last.Detail["stacked_on"] != "COACH-1" {
		t.Fatalf("event: %+v", last)
	}
}

// COACH-1's hand ran with a dirty working tree: another task's untracked files and a tracked
// file changed outside COACH-1's commits. With a scope set before the hand ran, that dirt was
// never among its inputs; any other input missing at the point blocks the stack unless a human
// accepts it (TestRestackIgnoreNeedsTheHuman); a file of its own changed afterwards is stale.
func TestStackIgnoresForeignDirtAtThePoint(t *testing.T) {
	build := dirtyStack
	ws, point := build(t, []string{"lib/**"}, nil, nil)
	got, pred, err := ws.RestackPoint("COACH-2")
	if err != nil || got != point || pred.ID != "COACH-1" {
		t.Fatalf("foreign dirt outside the scope blocked the restack: %s %v", got, err)
	}
	// The hand ran before COACH-1 had a scope: its inputs hold the dirt. Narrowing the scope
	// afterwards does not hide it: it is listed for a human to accept.
	ws, _ = build(t, nil, nil, nil)
	c1, _ := ws.Store.Load("COACH-1")
	c1.Scope = []string{"lib/**"}
	ws.Store.Save(c1)
	ws = reopen(t, ws.Repo.Root)
	if _, _, err := ws.RestackPoint("COACH-2"); err == nil {
		t.Fatal("scope narrowed after the hand hid its inputs")
	}
	if plan, err := ws.PlanRestack("COACH-2"); err != nil || !slices.Contains(plan.Ignore, "Podfile.lock") {
		t.Fatalf("scope declared after the hand: %+v %v", plan, err)
	}
	// Without a scope the dirt cannot be told from the task's own files: it blocks unless a
	// human accepts that exact list (TestRestackIgnoreNeedsTheHuman).
	ws, _ = build(t, nil, nil, nil)
	if _, _, err := ws.RestackPoint("COACH-2"); err == nil || !strings.Contains(err.Error(), "ignored") {
		t.Fatalf("scope-less task with dirt restacked: %v", err)
	}
	if c2, _ := ws.Store.Load("COACH-2"); c2.StackedOn != "" {
		t.Fatal("stacked without a human")
	}
	// A file of COACH-1 changed after its hand ran, then committed: stale at the point.
	ws, _ = build(t, []string{"lib/**"}, nil, func(dir string) { swrite(t, dir, "lib/a.txt", "one, edited after the review\n") })
	if _, _, err := ws.RestackPoint("COACH-2"); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("own file edited after the hand: %v", err)
	}
	// In its scope, seen by the hand, but never committed: the point does not hold what the
	// hand checked.
	ws, _ = build(t, []string{"lib/**"},
		func(dir string) { swrite(t, dir, "lib/extra.txt", "only on disk\n") },
		func(dir string) { os.Remove(filepath.Join(dir, "lib/extra.txt")) })
	if _, _, err := ws.RestackPoint("COACH-2"); err == nil {
		t.Fatal("scoped file the hand saw but the point lacks was ignored")
	}
	if plan, err := ws.PlanRestack("COACH-2"); err != nil || strings.Join(plan.Ignore, ",") != "lib/extra.txt" {
		t.Fatalf("scoped file the hand saw but the point lacks: %+v %v", plan, err)
	}
}

// dirtyStack: COACH-1's hand ran with Podfile.lock changed and tool/wakeword/** untracked
// (another task's work), then only lib/a.txt was committed; COACH-2 (from main) has b.txt.
func dirtyStack(t *testing.T, scope []string, beforeReview, afterReview func(dir string)) (*core.Workspace, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "demo")
	os.MkdirAll(dir, 0o755)
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "h@x.y"}, {"config", "user.name", "H"}, {"config", "core.hooksPath", ".git/hooks"}} {
		sgit(t, dir, a...)
	}
	swrite(t, dir, ".truehand/config.yaml", stackConfig)
	swrite(t, dir, "Podfile.lock", "v1\n")
	commitAll(t, dir, "chore: truehand")
	sgit(t, dir, "checkout", "-q", "-b", "feat")
	ws := reopen(t, dir)
	c1, _ := ws.NewTask("COACH-1", "first", "", scope...)
	swrite(t, dir, "lib/a.txt", "one\n")
	swrite(t, dir, "Podfile.lock", "v2, another task's\n")
	swrite(t, dir, "tool/wakeword/data/x.bin", "untracked\n")
	if beforeReview != nil {
		beforeReview(dir)
	}
	review(t, ws, c1)
	if afterReview != nil {
		afterReview(dir)
	}
	sgit(t, dir, "add", "lib", ".truehand")
	c1.State = board.Reviewed
	ws.Store.Save(c1)
	sgit(t, dir, "add", ".truehand")
	sgit(t, dir, "commit", "-q", "-m", "feat: one")
	point := sgit(t, dir, "rev-parse", "HEAD")
	main := sgit(t, dir, "rev-parse", "main")
	ws.Store.Create(&board.Task{ID: "COACH-2", Title: "second", State: board.Building, Base: main, BaseRef: "main"})
	swrite(t, dir, "lib/b.txt", "two\n")
	sgit(t, dir, "add", "lib/b.txt", ".truehand")
	sgit(t, dir, "commit", "-q", "-m", "feat: two")
	return reopen(t, dir), point
}

// Scope-less COACH-1 with dirt: the plan lists the dirt; only a human accepting exactly that
// list stacks, and only an event by that human makes the list count.
func TestRestackIgnoreNeedsTheHuman(t *testing.T) {
	ws, point := dirtyStack(t, nil, nil, nil)
	plan, err := ws.PlanRestack("COACH-2")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Podfile.lock", "tool/wakeword/data/x.bin"}
	if plan.Point != point || plan.Pred.ID != "COACH-1" || strings.Join(plan.Ignore, ",") != strings.Join(want, ",") || plan.IgnoreHash != core.IgnoreHash(want) {
		t.Fatalf("plan: %+v", plan)
	}
	for name, h := range map[string]string{
		"none":     "",
		"one more": core.IgnoreHash(append(want, "lib/a.txt")),
		"one less": core.IgnoreHash(want[:1]),
	} {
		if _, err := ws.RestackWith("COACH-2", point, "COACH-1", h, "human:h@x.y", nil); err == nil {
			t.Errorf("ignore list %s accepted", name)
		}
	}
	if _, err := ws.RestackWith("COACH-2", point, "COACH-1", plan.IgnoreHash, "claude:agent", nil); err == nil {
		t.Fatal("an agent accepted files to ignore")
	}
	if _, err := ws.RestackWith("COACH-2", point, "COACH-1", plan.IgnoreHash, "human:h@x.y", nil); err != nil {
		t.Fatal(err)
	}
	ws = reopen(t, ws.Repo.Root)
	c2, _ := ws.Store.Load("COACH-2")
	if base, err := ws.BaseOf(c2); err != nil || base != point || strings.Join(c2.StackedIgnore, ",") != strings.Join(want, ",") {
		t.Fatalf("after the human's restack: %s %v %v", base, err, c2.StackedIgnore)
	}
	c1, _ := ws.Store.Load("COACH-1")
	if res, err := ws.Check(c1, board.Verified); err != nil || !res.Pass {
		t.Fatalf("COACH-1 at the point with the accepted list: %+v %v", res, err)
	}
	evs, _ := ws.Store.Events("COACH-2")
	if last := evs[len(evs)-1]; last.Detail["ignore_hash"] != plan.IgnoreHash || last.Actor != "human:h@x.y" {
		t.Fatalf("event: %+v", last)
	}

	// The same list written into task.yaml by an agent, with its own event, counts for nothing.
	ws2, point2 := dirtyStack(t, nil, nil, nil)
	ws2.Store.Create(&board.Task{ID: "COACH-3", Title: "x", State: board.Approved, Base: point2, BaseRef: "main", StackedOn: "COACH-1", StackedIgnore: want})
	ws2.Store.AppendEvent("COACH-3", board.Event{Type: "restacked", Actor: "claude:agent", Detail: map[string]any{"base": point2, "stacked_on": "COACH-1", "ignore_hash": core.IgnoreHash(want)}})
	ws2 = reopen(t, ws2.Repo.Root)
	c3, _ := ws2.Store.Load("COACH-3")
	if _, err := ws2.BaseOf(c3); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("agent-written ignore list: %v", err)
	}
	if tip, _ := ws2.TipOf(func() *board.Task { c, _ := ws2.Store.Load("COACH-1"); return c }()); tip != "" {
		t.Fatalf("agent-written ignore list closed COACH-1 at %s", tip)
	}
}

// The reviewer's case: the file that made COACH-1's hand pass was never committed. It is not
// silently ignored: it shows in the list a human has to read and accept.
func TestRestackListsTheUncommittedFileTheHandNeeded(t *testing.T) {
	ws, _ := dirtyStack(t, nil,
		func(dir string) { swrite(t, dir, "lib/a_fix.txt", "what made it pass\n") },
		func(dir string) { os.Remove(filepath.Join(dir, "lib/a_fix.txt")) })
	if _, _, err := ws.RestackPoint("COACH-2"); err == nil {
		t.Fatal("restacked without a human over a file the hand needed")
	}
	plan, err := ws.PlanRestack("COACH-2")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(plan.Ignore, "lib/a_fix.txt") {
		t.Fatalf("lib/a_fix.txt missing from the list to accept: %v", plan.Ignore)
	}
}

// A new task never stacks over dirt by itself: that takes a human's restack.
func TestNewTaskDoesNotStackOverDirt(t *testing.T) {
	ws, point := dirtyStack(t, nil, nil, nil)
	sgit(t, ws.Repo.Root, "reset", "-q", "--hard", point)
	ws = reopen(t, ws.Repo.Root)
	c, err := ws.NewTask("COACH-3", "third", "")
	if err != nil {
		t.Fatal(err)
	}
	if c.StackedOn != "" {
		t.Fatalf("stacked over dirt on creation: %+v", c)
	}
}

// CI honours the list a human accepted: COACH-1, judged at the point with its dirt ignored,
// covers its files; COACH-2 the rest.
func TestCIHonoursTheAcceptedIgnoreList(t *testing.T) {
	ws, point := dirtyStack(t, nil, nil, nil)
	plan, err := ws.PlanRestack("COACH-2")
	if err != nil || len(plan.Ignore) == 0 {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	if _, err := ws.RestackWith("COACH-2", point, "COACH-1", plan.IgnoreHash, "human:h@x.y", nil); err != nil {
		t.Fatal(err)
	}
	ws = reopen(t, ws.Repo.Root)
	c2, _ := ws.Store.Load("COACH-2")
	review(t, ws, c2)
	for _, id := range []string{"COACH-1", "COACH-2"} {
		x, _ := ws.Store.Load(id)
		x.State = board.Delivered
		ws.Store.Save(x)
	}
	sgit(t, ws.Repo.Root, "add", ".truehand")
	sgit(t, ws.Repo.Root, "commit", "-q", "-m", "chore: delivered")
	r, err := ci.Verify(reopen(t, ws.Repo.Root), "main")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Pass {
		t.Fatalf("CI refused the chain with an accepted ignore list: %v %v", r.Problems, r.Uncovered)
	}
}

// threeStack: COACH-1 (no scope) reviewed with Podfile.lock dirt, and, when wip, COACH-2's
// file already in the folder; COACH-2 (scope lib/c2.txt) stacked on it by a human and
// delivered, committing out.txt too when outside; COACH-3 made the old way, from main.
func threeStack(t *testing.T, outside, wip bool) (*core.Workspace, string, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "demo")
	os.MkdirAll(dir, 0o755)
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "h@x.y"}, {"config", "user.name", "H"}, {"config", "core.hooksPath", ".git/hooks"}} {
		sgit(t, dir, a...)
	}
	swrite(t, dir, ".truehand/config.yaml", stackConfig)
	swrite(t, dir, "Podfile.lock", "v1\n")
	commitAll(t, dir, "chore: truehand")
	main := sgit(t, dir, "rev-parse", "HEAD")
	sgit(t, dir, "checkout", "-q", "-b", "feat")
	ws := reopen(t, dir)
	c1, _ := ws.NewTask("COACH-1", "first", "")
	swrite(t, dir, "lib/a.txt", "one\n")
	swrite(t, dir, "Podfile.lock", "v2, another task's\n")
	if wip {
		swrite(t, dir, "lib/c2.txt", "two, in progress\n")
	}
	review(t, ws, c1)
	c1.State = board.Reviewed
	ws.Store.Save(c1)
	sgit(t, dir, "add", "lib/a.txt", ".truehand")
	sgit(t, dir, "commit", "-q", "-m", "feat: one")
	p1 := sgit(t, dir, "rev-parse", "HEAD")

	ws.Store.Create(&board.Task{ID: "COACH-2", Title: "second", State: board.Building, Base: main, BaseRef: "main", Scope: []string{"lib/c2.txt"}})
	swrite(t, dir, "lib/c2.txt", "two\n")
	add := []string{"lib/c2.txt", ".truehand"}
	if outside {
		swrite(t, dir, "out.txt", "nobody's\n")
		add = append(add, "out.txt")
	}
	sgit(t, dir, append([]string{"add"}, add...)...)
	sgit(t, dir, "commit", "-q", "-m", "feat: two")
	ws = reopen(t, dir)
	plan, err := ws.PlanRestack("COACH-2")
	if wip {
		if err == nil && slices.Contains(plan.Ignore, "lib/c2.txt") {
			t.Fatalf("COACH-2's committed file offered as dirt to ignore: %v", plan.Ignore)
		}
		return ws, p1, ""
	}
	if err != nil || plan.Point != p1 {
		t.Fatalf("COACH-2 plan: %+v %v", plan, err)
	}
	if _, err := ws.RestackWith("COACH-2", p1, "COACH-1", plan.IgnoreHash, "human:h@x.y", nil); err != nil {
		t.Fatal(err)
	}
	ws = reopen(t, dir)
	c2, _ := ws.Store.Load("COACH-2")
	review(t, ws, c2)
	c2.State = board.Delivered
	ws.Store.Save(c2)
	sgit(t, dir, "add", ".truehand")
	sgit(t, dir, "commit", "-q", "-m", "chore: two delivered")
	tip2 := sgit(t, dir, "rev-parse", "HEAD")

	ws.Store.Create(&board.Task{ID: "COACH-3", Title: "third", State: board.Approved, Base: main, BaseRef: "main", Scope: []string{"lib/c3.txt"}})
	swrite(t, dir, "lib/c3.txt", "three\n")
	sgit(t, dir, "add", "lib/c3.txt", ".truehand")
	sgit(t, dir, "commit", "-q", "-m", "feat: three")
	return reopen(t, dir), p1, tip2
}

// Three tasks stacked: COACH-3 stacks on COACH-2's tip, through the chain COACH-2 → COACH-1,
// and nothing COACH-2 committed is ever offered as dirt to ignore.
func TestThreeLevelStack(t *testing.T) {
	ws, _, tip2 := threeStack(t, false, false)
	plan, err := ws.PlanRestack("COACH-3")
	if err != nil || plan.Point != tip2 || plan.Pred.ID != "COACH-2" || len(plan.Ignore) != 0 {
		t.Fatalf("COACH-3 plan: %+v %v (want %s on COACH-2, nothing to ignore)", plan, err, tip2)
	}
	if _, err := ws.RestackWith("COACH-3", plan.Point, "COACH-2", plan.IgnoreHash, "human:h@x.y", nil); err != nil {
		t.Fatal(err)
	}
	ws = reopen(t, ws.Repo.Root)
	c3, _ := ws.Store.Load("COACH-3")
	fp, err := ws.Fingerprint(c3)
	if err != nil || len(fp.Files) != 1 || fp.Files["lib/c3.txt"] == "" {
		t.Fatalf("COACH-3's change: %v %v", fp.Files, err)
	}

	// COACH-2 committed a file outside its scope: no point on COACH-2, and none below it either
	// (that would hand COACH-2's commits to COACH-3): the plan fails and says why.
	ws, _, _ = threeStack(t, true, false)
	if plan, err := ws.PlanRestack("COACH-3"); err == nil {
		t.Fatalf("COACH-3 stacked over COACH-2's uncovered out.txt: %+v", plan)
	} else if !strings.Contains(err.Error(), "out.txt") {
		t.Fatalf("plan error does not name the uncovered file: %v", err)
	}

	// COACH-1's hand saw COACH-2's file in progress: that file is committed later, so it is
	// never dirt (threeStack fails if it is offered).
	threeStack(t, false, true)
}
