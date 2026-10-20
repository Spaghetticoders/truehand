package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/humanauth"
)

const bypassConfig = `version: 0
base: main
commands:
  check: "test -f greet.txt"
  slow: "sleep 30 & echo $! > child.pid; wait"
gates:
  verified:
    - check: hand_passed
      with: { hand: check }
  reviewed:
    - check: review_by_other_role
  delivered:
    - check: evidence_matches_head
    - check: conventional_commits
`

func bypassSandbox(t *testing.T) *sandbox {
	s := newSandbox(t)
	s.must("init", "--stack", "generic")
	s.write(".truehand/config.yaml", bypassConfig)
	s.git("add", ".")
	s.git("commit", "-q", "-m", "chore: truehand")
	s.git("checkout", "-q", "-b", "feat/x")
	s.must("task", "new", "T-1", "--title", "x")
	s.must("task", "move", "approved")
	return s
}

func writeNotes(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "r.md")
	os.WriteFile(p, []byte("Read the diff and the evidence; ran the check myself.\n"), 0o644)
	return p
}

// Finding 1: a configured hand runs exactly its configured command.
func TestHandNameBoundToCommand(t *testing.T) {
	s := bypassSandbox(t)
	s.must("task", "move", "building")
	for _, args := range [][]string{
		{"hand", "run", "check", "--quiet", "--", "true"},
		{"hand", "run", "test-proof", "--quiet", "--", "true"},
		{"hand", "run", "review", "--quiet", "--", "true"},
		{"hand", "run", "eyes", "--quiet", "--", "true"},
	} {
		if code, out := s.run("", args...); code == 0 {
			t.Fatalf("%v must be refused:\n%s", args, out)
		}
	}
	s.fails(3, "task", "move", "verified")

	// Evidence from an older command no longer counts once the config changes.
	s.write("greet.txt", "hi\n")
	s.must("hand", "run", "check", "--quiet")
	s.write(".truehand/config.yaml", strings.Replace(bypassConfig, `"test -f greet.txt"`, `"grep -q hello greet.txt"`, 1))
	out := s.fails(3, "gate", "check", "verified")
	if !strings.Contains(out, "but the configured command is") {
		t.Fatalf("expected command mismatch:\n%s", out)
	}
}

// Finding 1 (config side): a gate on a hand with no configured command is a config error.
func TestHandGateNeedsConfiguredCommand(t *testing.T) {
	s := newSandbox(t)
	s.must("init", "--stack", "generic")
	s.write(".truehand/config.yaml", "version: 0\ngates:\n  verified:\n    - check: hand_passed\n      with: { hand: lint }\n")
	code, out := s.run("", "task", "list")
	if code == 0 || !strings.Contains(out, "needs commands.lint") {
		t.Fatalf("expected config error, got %d:\n%s", code, out)
	}
}

// Finding 2: approval is refused inside an agent session or with TRUEHAND_ACTOR set.
func TestApproveRefusedForAgents(t *testing.T) {
	s := newSandbox(t)
	s.must("init", "--stack", "generic")
	s.write(".truehand/config.yaml", "version: 0\ngates: {}\n")
	s.must("task", "new", "T-1", "--title", "x")
	s.write(".truehand/tasks/T-1/plan.md", validPlan)
	withTTY(t, true)

	t.Setenv("TRUEHAND_ACTOR", "agent:dev")
	if code, out := s.run("T-1\n", "approve"); code == 0 || !strings.Contains(out, "TRUEHAND_ACTOR") {
		t.Fatalf("approve with TRUEHAND_ACTOR must fail: %d\n%s", code, out)
	}
	t.Setenv("TRUEHAND_ACTOR", "")

	old := core.AgentDetector
	core.AgentDetector = func() int { return 4242 }
	code, out := s.run("T-1\n", "approve")
	core.AgentDetector = old
	if code == 0 || !strings.Contains(out, "agent session") {
		t.Fatalf("approve inside an agent session must fail: %d\n%s", code, out)
	}
	if code, out := s.run("T-1\n", "approve"); code != 0 {
		t.Fatalf("a human at a terminal can approve: %d\n%s", code, out)
	}
}

// Finding 3: whoever ran the hands cannot review, even if a human moved the task.
func TestReviewerMustNotHaveWorked(t *testing.T) {
	s := bypassSandbox(t)
	t.Setenv("TRUEHAND_ACTOR", "")
	s.must("task", "move", "building") // a human moves it
	t.Setenv("TRUEHAND_ACTOR", "agent:dev")
	s.write("greet.txt", "hi\n")
	s.must("hand", "run", "check", "--quiet")
	s.must("task", "move", "verified")
	s.must("review", "--verdict", "pass", "--notes", writeNotes(t))
	out := s.fails(3, "task", "move", "reviewed")
	if !strings.Contains(out, "agent:dev, who worked on this task") {
		t.Fatalf("the agent that ran the hands must not review:\n%s", out)
	}
	t.Setenv("TRUEHAND_ACTOR", "agent:reviewer")
	s.must("review", "--verdict", "pass", "--notes", writeNotes(t))
	s.must("task", "move", "reviewed")
}

// Finding 4: config typos are errors, not silently dropped rules.
func TestConfigTyposRejected(t *testing.T) {
	s := newSandbox(t)
	s.must("init", "--stack", "generic")
	for _, cfg := range []string{
		"version: 0\ngate:\n  verified: []\n",
		"version: 0\ngates:\n  verified:\n    - check: plan_valid\n      whne: { changed: [x] }\n",
		"version: 0\n",
	} {
		s.write(".truehand/config.yaml", cfg)
		if code, out := s.run("", "task", "list"); code == 0 {
			t.Fatalf("config %q must be rejected:\n%s", cfg, out)
		}
	}
}

// Finding 4 (CI side): CI ignores task.yaml's state and judges with the base branch's gates.
func TestCIIgnoresStateAndBranchConfig(t *testing.T) {
	s := bypassSandbox(t)
	s.write("greet.txt", "hi\n")
	// Skip every gate by editing files, then relax the gates on the branch.
	tk := readFile(t, filepath.Join(s.dir, ".truehand/tasks/T-1/task.yaml"))
	s.write(".truehand/tasks/T-1/task.yaml", strings.Replace(tk, "state: approved", "state: delivered", 1))
	s.write(".truehand/config.yaml", "version: 0\nbase: main\ngates: {}\n")
	s.git("add", ".")
	s.git("commit", "-q", "-m", "feat: greet")
	out := s.fails(3, "ci", "verify", "--base", "main")
	if !strings.Contains(out, "gates from main") || !strings.Contains(out, "hand_passed") {
		t.Fatalf("CI must use main's gates and evaluate them all:\n%s", out)
	}
}

// Finding 5: delivered requires the evidence to describe what is committed.
func TestDeliveredNeedsCommittedEvidence(t *testing.T) {
	s := bypassSandbox(t)
	t.Setenv("TRUEHAND_ACTOR", "agent:dev")
	s.must("task", "move", "building")
	s.write("greet.txt", "hi\n")
	s.write("needed.txt", "x\n")
	s.must("hand", "run", "check", "--quiet")
	s.must("task", "move", "verified")
	t.Setenv("TRUEHAND_ACTOR", "agent:reviewer")
	s.must("review", "--verdict", "pass", "--notes", writeNotes(t))
	s.must("task", "move", "reviewed")
	s.git("add", "greet.txt")
	s.git("commit", "-q", "-m", "feat: greet")
	out := s.fails(3, "task", "move", "delivered")
	if !strings.Contains(out, "uncommitted changes in task files: needed.txt") {
		t.Fatalf("expected uncommitted file refusal:\n%s", out)
	}
	s.git("add", "needed.txt")
	s.git("commit", "-q", "-m", "feat: needed")
	s.must("task", "move", "delivered")
}

// Finding 6: a later failing run is not hidden by an earlier pass.
func TestLatestFailBlocksDelivery(t *testing.T) {
	s := bypassSandbox(t)
	s.must("task", "move", "building")
	s.write("greet.txt", "hi\n")
	s.must("hand", "run", "check", "--quiet")
	s.write(".truehand/config.yaml", strings.Replace(bypassConfig, `"test -f greet.txt"`, `"test -f missing.txt"`, 1))
	s.fails(3, "hand", "run", "check", "--quiet")
	s.write(".truehand/config.yaml", bypassConfig)
	out := s.fails(3, "gate", "check", "delivered")
	if !strings.Contains(out, "verdict is fail") {
		t.Fatalf("expected the latest fail to block delivery:\n%s", out)
	}
}

// Finding 7: after a rebase onto a newer main, other people's commits are not the task's.
func TestRebaseKeepsTaskDiffClean(t *testing.T) {
	s := bypassSandbox(t)
	s.write("greet.txt", "hi\n")
	s.git("add", "greet.txt")
	s.git("commit", "-q", "-m", "feat: greet")
	s.git("checkout", "-q", "main")
	s.write("upstream.txt", "theirs\n")
	s.git("add", "upstream.txt")
	s.git("commit", "-q", "-m", "WIP someone else")
	s.git("checkout", "-q", "feat/x")
	s.git("rebase", "-q", "main")
	_, out := s.run("", "gate", "check", "delivered", "--json")
	if strings.Contains(out, "WIP someone else") || strings.Contains(out, "upstream.txt") {
		t.Fatalf("upstream commit leaked into the task:\n%s", out)
	}
	if !strings.Contains(out, `"check": "conventional_commits",
      "status": "pass"`) {
		t.Fatalf("conventional_commits should only see the task's commit:\n%s", out)
	}
}

// Finding 7 (a): a base ref that does not exist is an error, not a silent fallback.
func TestMissingBaseRefIsAnError(t *testing.T) {
	s := newSandbox(t)
	s.must("init", "--stack", "generic")
	s.write(".truehand/config.yaml", "version: 0\nbase: origin/main\ngates: {}\n")
	code, out := s.run("", "task", "new", "T-1", "--title", "x")
	if code == 0 || !strings.Contains(out, `base ref "origin/main" does not exist`) {
		t.Fatalf("expected base ref error: %d\n%s", code, out)
	}
}

// Finding 8: the timeout kills the whole process tree.
func TestTimeoutKillsChildren(t *testing.T) {
	s := bypassSandbox(t)
	start := time.Now()
	code, out := s.run("", "hand", "run", "slow", "--quiet", "--timeout", "1s")
	if took := time.Since(start); took > 2500*time.Millisecond {
		t.Fatalf("timeout of 1s took %s", took)
	}
	if code == 0 || !strings.Contains(out, "timed out") {
		t.Fatalf("expected a timeout: %d\n%s", code, out)
	}
	pid := strings.TrimSpace(readFile(t, filepath.Join(s.dir, "child.pid")))
	time.Sleep(100 * time.Millisecond)
	if err := exec.Command("kill", "-0", pid).Run(); err == nil {
		exec.Command("kill", pid).Run()
		t.Fatalf("grandchild %s survived the timeout", pid)
	}
}

// Finding 10: concurrent hands get distinct records.
func TestConcurrentHandsDistinctIDs(t *testing.T) {
	s := bypassSandbox(t)
	s.write("greet.txt", "hi\n")
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.run("", "hand", "run", "check", "--quiet")
		}()
	}
	wg.Wait()
	out := s.must("evidence", "list", "--all")
	if strings.Contains(out, "TAMPERED") {
		t.Fatalf("concurrent runs overwrote each other:\n%s", out)
	}
	for _, id := range []string{"check-1", "check-2", "check-3", "check-4"} {
		if !strings.Contains(out, id) {
			t.Fatalf("missing %s:\n%s", id, out)
		}
	}
}

// Round 2, A1: an agent cannot start serve with approvals; a failed OS auth means no approvals.
func TestServeApprovalsNeedAHuman(t *testing.T) {
	s := newSandbox(t)
	s.must("init", "--stack", "generic")
	// serve blocks while it listens, so a missing refusal shows up as a timeout.
	runServe := func() (int, string) {
		type res struct {
			code int
			out  string
		}
		ch := make(chan res, 1)
		go func() { c, o := s.run("", "serve", "--addr", "127.0.0.1:0"); ch <- res{c, o} }()
		select {
		case r := <-ch:
			return r.code, r.out
		case <-time.After(3 * time.Second):
			t.Fatal("serve started instead of refusing")
			return 0, ""
		}
	}
	old := core.AgentDetector
	core.AgentDetector = func() int { return 4242 }
	code, out := runServe()
	core.AgentDetector = old
	if code == 0 || !strings.Contains(out, "--read-only") {
		t.Fatalf("serve with approvals inside an agent session must fail: %d\n%s", code, out)
	}
	oldAuth := humanauth.Authenticate
	humanauth.Authenticate = func(string) error { return errors.New("authentication cancelled") }
	defer func() { humanauth.Authenticate = oldAuth }()
	code, out = runServe()
	if code == 0 || !strings.Contains(out, "cancelled") {
		t.Fatalf("serve must not enable approvals without authentication: %d\n%s", code, out)
	}
}

// Round 2, A3: approve needs the OS authentication even with a TTY.
func TestApproveNeedsOSAuth(t *testing.T) {
	s := newSandbox(t)
	s.must("init", "--stack", "generic")
	s.write(".truehand/config.yaml", "version: 0\ngates:\n  approved:\n    - check: human_approval\n")
	s.must("task", "new", "T-1", "--title", "x")
	s.write(".truehand/tasks/T-1/plan.md", validPlan)
	withTTY(t, true)
	oldAuth := humanauth.Authenticate
	humanauth.Authenticate = func(string) error { return errors.New("authentication cancelled") }
	code, out := s.run("T-1\n", "approve")
	humanauth.Authenticate = oldAuth
	if code == 0 {
		t.Fatalf("approve without OS auth must fail:\n%s", out)
	}
	s.fails(3, "task", "move", "approved")
	if code, out := s.run("T-1\n", "approve"); code != 0 {
		t.Fatal(out)
	}
	out = s.must("task", "show", "--json")
	if !strings.Contains(out, `"via": "os-auth`) {
		t.Fatalf("approval must record the OS auth method:\n%s", out)
	}
}

// Round 2, A2: test-proof runs exactly the configured test command.
func TestTestProofBoundToCommand(t *testing.T) {
	s := proofSandbox(t, "sh test/check.sh")
	s.write("lib/code.txt", "fixed\n")
	s.write("test/check.sh", "grep -q fixed lib/code.txt\n")
	code, out := s.run("", "hand", "test-proof", "--quiet", "--command", "test -f lib/code.txt")
	if code == 0 || !strings.Contains(out, "configured") {
		t.Fatalf("--command must be refused when commands.test is set: %d\n%s", code, out)
	}
	s.must("hand", "test-proof", "--quiet")
	s.write(".truehand/config.yaml", "version: 0\nbase: main\ncommands:\n  test: \"sh test/other.sh\"\ngates:\n  verified:\n    - check: test_proof\n")
	out = s.fails(3, "gate", "check", "T-P", "verified")
	if !strings.Contains(out, "configured command") {
		t.Fatalf("a proof with another command must not count:\n%s", out)
	}
}

// Round 2, B3: a failing ad-hoc hand does not block delivery.
func TestAdHocHandsDoNotBlockDelivery(t *testing.T) {
	s := bypassSandbox(t)
	t.Setenv("TRUEHAND_ACTOR", "agent:dev")
	s.must("task", "move", "building")
	s.write("greet.txt", "hi\n")
	s.fails(3, "hand", "run", "repro", "--quiet", "--", "false")
	s.must("hand", "run", "check", "--quiet")
	s.must("task", "move", "verified")
	t.Setenv("TRUEHAND_ACTOR", "agent:reviewer")
	s.must("review", "--verdict", "pass", "--notes", writeNotes(t))
	s.must("task", "move", "reviewed")
	s.git("add", "greet.txt")
	s.git("commit", "-q", "-m", "feat: greet")
	s.must("task", "move", "delivered")
}

// Round 2, A4 residual: a session that only edited code is a worker and cannot review.
func TestEditorCannotReview(t *testing.T) {
	s := bypassSandbox(t)
	t.Setenv("TRUEHAND_ACTOR", "agent:hands")
	s.must("task", "move", "building")
	s.write("greet.txt", "hi\n")
	in := fmt.Sprintf(`{"hook_event_name":"PreToolUse","session_id":"sess-writer","cwd":%q,"tool_name":"Write","tool_input":{"file_path":%q}}`, s.dir, filepath.Join(s.dir, "greet.txt"))
	if code, out := s.run(in, "hook", "claude"); code != 0 || strings.TrimSpace(out) != "" {
		t.Fatalf("the write itself is allowed: %d %s", code, out)
	}
	s.must("hand", "run", "check", "--quiet")
	s.must("task", "move", "verified")
	t.Setenv("TRUEHAND_ACTOR", "claude:sess-writer")
	s.must("review", "--verdict", "pass", "--notes", writeNotes(t))
	out := s.fails(3, "task", "move", "reviewed")
	if !strings.Contains(out, "claude:sess-writer, who worked on this task") {
		t.Fatalf("the session that wrote the code must not review:\n%s", out)
	}
}

// Round 2, B1: a broken config keeps the locks closed.
func TestHooksFailClosedOnBrokenConfig(t *testing.T) {
	s := newSandbox(t)
	s.must("init", "--stack", "generic")
	s.write(".truehand/config.yaml", "version: 0\ngates:\n  verified:\n    - check: plan_valid\n      whne: {}\n")
	hook := func(tool string, input map[string]any) string {
		b, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "session_id": "s", "cwd": s.dir, "tool_name": tool, "tool_input": input})
		_, out := s.run(string(b), "hook", "claude")
		return out
	}
	for _, c := range []struct {
		tool  string
		input map[string]any
		deny  bool
	}{
		{"Bash", map[string]any{"command": "git push"}, true},
		{"Bash", map[string]any{"command": "git commit -m 'feat: x'"}, true},
		{"Write", map[string]any{"file_path": filepath.Join(s.dir, ".truehand/tasks/T-1/approvals.jsonl")}, true},
		{"Bash", map[string]any{"command": "ls"}, false},
		{"Write", map[string]any{"file_path": filepath.Join(s.dir, "lib/a.go")}, false},
	} {
		out := hook(c.tool, c.input)
		if strings.Contains(out, `"deny"`) != c.deny {
			t.Errorf("%s %v: deny=%v, got %s", c.tool, c.input, c.deny, out)
		}
	}
}

func TestStopExplainsBrokenConfig(t *testing.T) {
	s := newSandbox(t)
	s.must("init", "--stack", "generic")
	s.write(".truehand/config.yaml", "version: 0\ngate: {}\n")
	b, _ := json.Marshal(map[string]any{"hook_event_name": "Stop", "session_id": "s", "cwd": s.dir, "stop_hook_active": false})
	_, out := s.run(string(b), "hook", "claude")
	if !strings.Contains(out, `"decision":"block"`) || !strings.Contains(out, "config does not load") {
		t.Fatalf("Stop must tell the agent to report the broken config: %s", out)
	}
	b, _ = json.Marshal(map[string]any{"hook_event_name": "Stop", "session_id": "s", "cwd": s.dir, "stop_hook_active": true})
	if _, out := s.run(string(b), "hook", "claude"); strings.TrimSpace(out) != "" {
		t.Fatalf("never block twice: %s", out)
	}
}

// Tasks sharing a branch stay independent when they declare their files.
func TestScopedTasksShareABranch(t *testing.T) {
	s := proofSandbox(t, "sh test/check.sh")
	s.write("lib/other.txt", "v1\n")
	s.git("add", ".")
	s.git("commit", "-q", "-m", "chore: other")
	s.must("task", "new", "T-A", "--title", "a", "--scope", "lib/code.txt,test/check.sh")
	s.write("lib/code.txt", "fixed\n")
	s.write("test/check.sh", "grep -q fixed lib/code.txt\n")
	s.must("hand", "test-proof", "--quiet")
	s.git("add", ".")
	s.git("commit", "-q", "-m", "fix: code")

	// Another task's work on the same branch, committed or not, does not touch T-A.
	s.must("task", "new", "T-B", "--title", "b", "--scope", "lib/other.txt")
	s.write("lib/other.txt", "v2\n")
	s.write("lib/new.txt", "x\n")
	out := s.must("evidence", "list", "T-A")
	if strings.Contains(out, "STALE") {
		t.Fatalf("T-A's evidence must ignore files outside its scope:\n%s", out)
	}
	// test-proof reverts only the scope: lib/new.txt must survive and not break the run.
	s.must("task", "use", "T-A")
	out = s.must("hand", "test-proof", "--quiet")
	if !strings.Contains(out, "1 file(s) reverted") {
		t.Fatalf("only the scoped fix should be reverted:\n%s", out)
	}
	if readFile(t, filepath.Join(s.dir, "lib/new.txt")) != "x\n" {
		t.Fatal("files outside the scope must not be touched")
	}
}
