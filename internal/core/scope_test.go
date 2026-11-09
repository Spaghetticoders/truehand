package core

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/config"
)

const scopeConfig = "version: 0\nbase: main\ngates:\n  verified:\n    - check: in_scope\n"

func inScope(t *testing.T, ws *Workspace, id string) (bool, string) {
	t.Helper()
	task, _ := ws.Store.Load(id)
	res, err := ws.Check(task, board.Verified)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range res.Items {
		if it.Check == "in_scope" {
			return it.Status == "pass", it.Message
		}
	}
	t.Fatal("no in_scope item")
	return false, ""
}

func TestInScopeAndWidenScope(t *testing.T) {
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
	write(t, filepath.Join(dir, "lib/a.txt"), "a\n")
	write(t, filepath.Join(dir, "functions/index.js"), "f\n")
	if ok, msg := inScope(t, ws, "T-1"); ok || !strings.Contains(msg, "functions/index.js") || !strings.Contains(msg, "widen the scope") {
		t.Fatalf("file outside the scope: %v %s", ok, msg)
	}

	if plan, err := ws.PlanScope("T-1"); err != nil || strings.Join(plan.Outside, ",") != "functions/index.js" || strings.Join(plan.Add, ",") != "functions/index.js" {
		t.Fatalf("scope plan: %+v %v", plan, err)
	}
	// A planned task's scope (or a forged one) claims nothing.
	ws.NewTask("T-2", "y", "", "functions/**")
	if ok, _ := inScope(t, ws, "T-1"); ok {
		t.Fatal("a planned task's scope claimed functions/index.js")
	}
	// An approved task's scope claims it: tasks share the branch.
	t2, _ := ws.Store.Load("T-2")
	t2.State = board.Approved
	ws.Store.Save(t2)
	if ok, msg := inScope(t, ws, "T-1"); !ok {
		t.Fatalf("file claimed by another task: %s", msg)
	}
	ws.Store.Save(&board.Task{ID: "T-2", Title: "y", State: board.Planned, Scope: []string{"other/**"}})

	// Only a human widens, only adding, and then the gate passes.
	for name, actor := range map[string]string{"agent": "claude:abc", "empty": ""} {
		if _, err := ws.WidenScope("T-1", []string{"functions/index.js"}, actor, nil); err == nil {
			t.Errorf("%s widened the scope", name)
		}
	}
	for name, globs := range map[string][]string{"nothing new": {"lib/**"}, "empty": {" "}, "absolute": {"/etc/**"}, "negation": {"!lib/**"}, "newline": {"a\nb"}} {
		if _, err := ws.WidenScope("T-1", globs, "human:h@x.y", nil); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	for name, globs := range map[string][]string{"a pattern": {"functions/**"}, "a file not changed": {"other/x.js"}} {
		if _, err := ws.WidenScope("T-1", globs, "human:h@x.y", nil); err == nil {
			t.Errorf("widened with %s", name)
		}
	}
	got, err := ws.WidenScope("T-1", []string{"functions/index.js", "functions/index.js"}, "human:h@x.y", map[string]any{"via": "touchid"})
	if err != nil || strings.Join(got.Scope, ",") != "lib/**,functions/index.js" {
		t.Fatalf("widen: %v %v", got, err)
	}
	evs, _ := ws.Store.Events("T-1")
	if last := evs[len(evs)-1]; last.Type != "scope-widened" || last.Actor != "human:h@x.y" || last.Detail["via"] != "touchid" {
		t.Fatalf("event: %+v", last)
	}
	if ok, msg := inScope(t, ws, "T-1"); !ok {
		t.Fatalf("after widening: %s", msg)
	}

	// A task without a scope owns everything: nothing to widen, and the check passes.
	ws.NewTask("T-3", "z", "")
	if _, err := ws.WidenScope("T-3", []string{"x/**"}, "human:h@x.y", nil); err == nil {
		t.Fatal("widened a scope-less task, which would narrow it")
	}
	if ok, _ := inScope(t, ws, "T-3"); !ok {
		t.Fatal("scope-less task failed in_scope")
	}
}

// A task.yaml base past the merge-base, without a stack, would skip the branch's commits
// before it: no base at all instead (the stack lost by an older binary, on Wovio).
func TestBaseFallbackRefused(t *testing.T) {
	dir, _ := repo(t)
	git(t, dir, "checkout", "-q", "-b", "feat")
	write(t, filepath.Join(dir, "skipped.txt"), "x\n")
	git(t, dir, "add", "skipped.txt")
	git(t, dir, "commit", "-q", "-m", "feat: skipped")
	mid := git(t, dir, "rev-parse", "HEAD")
	write(t, filepath.Join(dir, "own.txt"), "y\n")
	git(t, dir, "add", "own.txt")
	git(t, dir, "commit", "-q", "-m", "feat: own")
	ws, _ := Open(dir)
	ws.Store.Create(&board.Task{ID: "T-1", Title: "x", State: board.Building, Base: mid, BaseRef: "main"})
	task, _ := ws.Store.Load("T-1")
	if b, err := ws.BaseOf(task); err == nil || !strings.Contains(err.Error(), "restack") {
		t.Fatalf("base past the merge-base accepted: %s %v", b, err)
	}
}
