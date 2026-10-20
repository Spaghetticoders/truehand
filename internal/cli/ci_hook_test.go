package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spaghetticoders/truehand/internal/core"
)

// deliveredBranch runs the full lifecycle of T-1 on branch feat/greet and commits everything.
func deliveredBranch(t *testing.T) *sandbox {
	s := newSandbox(t)
	s.must("init", "--stack", "generic")
	s.write(".truehand/config.yaml", testConfig)
	s.git("add", ".")
	s.git("commit", "-q", "-m", "chore: truehand")
	s.git("checkout", "-q", "-b", "feat/greet")
	s.must("task", "new", "T-1", "--title", "Add greeting")
	s.write(".truehand/tasks/T-1/plan.md", validPlan)
	withTTY(t, true)
	if code, out := s.run("T-1\n", "approve"); code != 0 {
		t.Fatal(out)
	}
	t.Setenv("TRUEHAND_ACTOR", "agent:dev")
	s.must("task", "move", "building")
	s.write("greet.txt", "hi\n")
	s.must("hand", "run", "check", "--quiet")
	s.must("task", "move", "verified")
	t.Setenv("TRUEHAND_ACTOR", "agent:reviewer")
	notes := filepath.Join(t.TempDir(), "r.md")
	os.WriteFile(notes, []byte("Checked greet.txt against the plan, fine.\n"), 0o644)
	s.must("review", "--verdict", "pass", "--notes", notes)
	s.must("task", "move", "reviewed")
	s.git("add", "greet.txt")
	s.git("commit", "-q", "-m", "feat: add greeting")
	s.must("task", "move", "delivered")
	s.git("add", ".truehand")
	s.git("commit", "-q", "-m", "chore: truehand evidence for T-1")
	return s
}

func TestCIVerify(t *testing.T) {
	s := deliveredBranch(t)
	out := s.must("ci", "verify", "--base", "main")
	if !strings.Contains(out, "PASS") {
		t.Fatalf("expected PASS:\n%s", out)
	}

	// A change outside any task fails the merge lock.
	s.write("sneaky.txt", "x\n")
	s.git("add", "sneaky.txt")
	s.git("commit", "-q", "-m", "feat: sneak")
	out = s.fails(3, "ci", "verify", "--base", "main")
	if !strings.Contains(out, "sneaky.txt") {
		t.Fatalf("expected uncovered sneaky.txt:\n%s", out)
	}
	s.git("reset", "-q", "--hard", "HEAD~1")

	// Changing verified content after delivery makes evidence stale.
	s.write("greet.txt", "changed\n")
	s.git("commit", "-q", "-am", "fix: tweak")
	out = s.fails(3, "ci", "verify", "--base", "main")
	if !strings.Contains(out, "stale") {
		t.Fatalf("expected stale evidence:\n%s", out)
	}
	s.git("reset", "-q", "--hard", "HEAD~1")

	// Forged evidence fails.
	logs, _ := filepath.Glob(filepath.Join(s.dir, ".truehand/tasks/T-1/evidence/check-*-output.log"))
	os.WriteFile(logs[0], []byte("forged"), 0o644)
	s.git("commit", "-q", "-am", "chore: forge")
	out = s.fails(3, "ci", "verify", "--base", "main")
	if !strings.Contains(out, "modified after it was recorded") {
		t.Fatalf("expected tamper detection:\n%s", out)
	}
}

func TestCIVerifyUndeliveredTask(t *testing.T) {
	s := newSandbox(t)
	s.must("init", "--stack", "generic")
	s.write(".truehand/config.yaml", testConfig)
	s.git("add", ".")
	s.git("commit", "-q", "-m", "chore: truehand")
	s.git("checkout", "-q", "-b", "feat/x")
	s.must("task", "new", "T-9", "--title", "x")
	s.write("x.txt", "x\n")
	s.git("add", ".")
	s.git("commit", "-q", "-m", "feat: x")
	out := s.fails(3, "ci", "verify", "--base", "main")
	if !strings.Contains(out, "T-9 is planned, not delivered") {
		t.Fatalf("expected undelivered task:\n%s", out)
	}
}

func TestCommitMsgHook(t *testing.T) {
	s := newSandbox(t)
	f := filepath.Join(t.TempDir(), "msg")
	for msg, ok := range map[string]bool{
		"feat(auth): add login\n\nbody": true,
		"# comment\nfix: thing":         true,
		"Merge branch 'x'":              true,
		"added stuff":                   false,
		"feat:missing space":            false,
	} {
		os.WriteFile(f, []byte(msg), 0o644)
		code, out := s.run("", "hook", "commit-msg", f)
		if (code == 0) != ok {
			t.Errorf("commit-msg %q: exit %d\n%s", msg, code, out)
		}
	}
}

func TestClaudeHookProtocol(t *testing.T) {
	s := newSandbox(t)
	s.must("init", "--stack", "generic")
	s.write(".truehand/config.yaml", testConfig)
	envFile := filepath.Join(t.TempDir(), "env")
	t.Setenv("CLAUDE_ENV_FILE", envFile)

	in := func(v map[string]any) string {
		v["cwd"] = s.dir
		v["session_id"] = "sess-1"
		b, _ := json.Marshal(v)
		return string(b)
	}
	code, out := s.run(in(map[string]any{"hook_event_name": "SessionStart", "source": "startup"}), "hook", "claude")
	if code != 0 || !strings.Contains(out, "additionalContext") {
		t.Fatalf("SessionStart: %d %s", code, out)
	}
	if b, _ := os.ReadFile(envFile); !strings.Contains(string(b), "export TRUEHAND_ACTOR='claude:sess-1'") {
		t.Fatalf("env file: %q", b)
	}

	code, out = s.run(in(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": "git push"}}), "hook", "claude")
	var resp struct {
		HookSpecificOutput struct {
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil || code != 0 || resp.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatalf("PreToolUse git push: %d %s", code, out)
	}

	code, out = s.run(in(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}}), "hook", "claude")
	if code != 0 || strings.TrimSpace(out) != "" {
		t.Fatalf("allowed command must produce no output: %d %q", code, out)
	}

	code, out = s.run(in(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Write", "tool_input": map[string]any{"file_path": filepath.Join(s.dir, ".truehand/config.yaml")}}), "hook", "claude")
	if !strings.Contains(out, "deny") {
		t.Fatalf("Write config.yaml must be denied: %s", out)
	}

	s.must("task", "new", "T-1", "--title", "x")
	s.write(".truehand/tasks/T-1/task.yaml", strings.Replace(readFile(t, filepath.Join(s.dir, ".truehand/tasks/T-1/task.yaml")), "state: planned", "state: building", 1))
	code, out = s.run(in(map[string]any{"hook_event_name": "Stop", "stop_hook_active": false}), "hook", "claude")
	if !strings.Contains(out, `"decision":"block"`) {
		t.Fatalf("Stop while building must block: %d %s", code, out)
	}
	code, out = s.run(in(map[string]any{"hook_event_name": "Stop", "stop_hook_active": true}), "hook", "claude")
	if strings.TrimSpace(out) != "" {
		t.Fatalf("Stop with stop_hook_active must not block: %s", out)
	}

	// Outside a truehand repo the hook is silent.
	other := t.TempDir()
	b, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "cwd": other, "tool_name": "Bash", "tool_input": map[string]any{"command": "git push"}})
	code, out = s.run(string(b), "hook", "claude")
	if code != 0 || strings.TrimSpace(out) != "" {
		t.Fatalf("non-truehand repo must be ignored: %d %s", code, out)
	}
	// Garbage input never blocks.
	if code, _ := s.run("not json", "hook", "claude"); code != 0 {
		t.Fatal("garbage input must not fail")
	}
}

func readFile(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCIVerifyUncoveredEarlierCommit(t *testing.T) {
	s := newSandbox(t)
	s.must("init", "--stack", "generic")
	// Trunk-based: the base is the branch itself, so a new task starts at HEAD and commits made
	// before it are not its work.
	s.write(".truehand/config.yaml", "version: 0\nbase: feat/x\ngates: {}\n")
	s.git("add", ".")
	s.git("commit", "-q", "-m", "chore: truehand")
	s.git("checkout", "-q", "-b", "feat/x")
	s.write("early.txt", "x\n")
	s.git("add", ".")
	s.git("commit", "-q", "-m", "feat: early")
	s.must("task", "new", "T-1", "--title", "x")
	for _, st := range []string{"approved", "building", "verified", "reviewed", "delivered"} {
		s.must("task", "move", st)
	}
	s.git("add", ".")
	s.git("commit", "-q", "-m", "chore: task")
	out := s.fails(3, "ci", "verify", "--base", "main")
	if !strings.Contains(out, "early.txt") || !strings.Contains(out, "not covered") {
		t.Fatalf("expected early.txt uncovered:\n%s", out)
	}
}

func TestMergedTaskIsDoneNotStale(t *testing.T) {
	s := deliveredBranch(t)
	board := func() core.BoardTask {
		var b core.BoardView
		if err := json.Unmarshal([]byte(s.must("board")), &b); err != nil {
			t.Fatal(err)
		}
		return b.Tasks[0]
	}
	if bt := board(); bt.Merged || bt.Stale {
		t.Fatalf("on its branch T-1 is neither merged nor stale: %+v", bt)
	}
	s.git("checkout", "-q", "main")
	s.git("merge", "-q", "--no-ff", "-m", "Merge branch 'feat/greet'", "feat/greet")
	s.write("later.txt", "another task's work\n")
	s.git("add", "later.txt")
	s.git("commit", "-q", "-m", "feat: later work")
	if bt := board(); !bt.Merged || bt.Stale {
		t.Fatalf("once merged, later commits must not make T-1 stale: %+v", bt)
	}
}
