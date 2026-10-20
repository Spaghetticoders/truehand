package cli

import (
	"bytes"

	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spaghetticoders/truehand/internal/core"
)

type sandbox struct {
	t   *testing.T
	dir string
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	dir := t.TempDir()
	s := &sandbox{t: t, dir: dir}
	s.git("init", "-q", "-b", "main")
	s.git("config", "user.email", "dev@example.com")
	s.git("config", "user.name", "Dev")
	s.write("README.md", "hello\n")
	s.git("add", ".")
	s.git("commit", "-q", "-m", "chore: initial")
	t.Setenv("TRUEHAND_ACTOR", "")
	return s
}

func (s *sandbox) git(args ...string) string {
	s.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = s.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		s.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func (s *sandbox) write(rel, content string) {
	s.t.Helper()
	p := filepath.Join(s.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

// run executes truehand and returns exit code and stdout+stderr.
func (s *sandbox) run(stdin string, args ...string) (int, string) {
	s.t.Helper()
	var out bytes.Buffer
	code := runIn(s.dir, args, strings.NewReader(stdin), &out, &out)
	return code, out.String()
}

func (s *sandbox) must(args ...string) string {
	s.t.Helper()
	code, out := s.run("", args...)
	if code != 0 {
		s.t.Fatalf("truehand %v: exit %d\n%s", args, code, out)
	}
	return out
}

func (s *sandbox) fails(code int, args ...string) string {
	s.t.Helper()
	got, out := s.run("", args...)
	if got != code {
		s.t.Fatalf("truehand %v: exit %d, want %d\n%s", args, got, code, out)
	}
	return out
}

const validPlan = `# T-1 — Add greeting

## Goal
Greeting exists.

## Scope
greet.txt

## Out of scope
Everything else.

## Steps
1. Write greet.txt — verify: hand check passes

## Verification
hand check.

## Risks
None.
`

const testConfig = `version: 0
base: main
commands:
  check: "test -f greet.txt"
  never-run: "false"
gates:
  approved:
    - check: plan_valid
    - check: human_approval
  verified:
    - check: hand_passed
      with: { hand: check }
    - check: hand_passed
      when: { changed: ["ui/**"] }
      with: { hand: never-run }
  reviewed:
    - check: review_by_other_role
  delivered:
    - check: evidence_matches_head
    - check: conventional_commits
`

func withTTY(t *testing.T, v bool) {
	old := isTTY
	isTTY = func() bool { return v }
	t.Cleanup(func() { isTTY = old })
}

func TestFullLifecycle(t *testing.T) {
	s := newSandbox(t)
	s.must("init", "--stack", "generic")
	s.write(".truehand/config.yaml", testConfig)
	s.git("checkout", "-q", "-b", "feat/greet")

	s.must("task", "new", "T-1", "--title", "Add greeting")
	if out := s.must("task", "use"); strings.TrimSpace(out) != "T-1" {
		t.Fatalf("active task = %q", out)
	}

	// approved: plan missing, then incomplete, then valid but unapproved.
	out := s.fails(3, "task", "move", "approved")
	if !strings.Contains(out, "plan.md not found") {
		t.Fatalf("expected missing plan, got:\n%s", out)
	}
	s.must("plan", "init")
	s.fails(3, "plan", "check")
	s.write(".truehand/tasks/T-1/plan.md", validPlan)
	s.must("plan", "check")
	out = s.fails(3, "task", "move", "approved")
	if !strings.Contains(out, "waiting for a human") {
		t.Fatalf("expected approval wait, got:\n%s", out)
	}

	// Agents have no TTY: approve must refuse.
	withTTY(t, false)
	if code, out := s.run("T-1\n", "approve"); code == 0 || !strings.Contains(out, "not a TTY") {
		t.Fatalf("approve without TTY should fail, got %d:\n%s", code, out)
	}
	withTTY(t, true)
	if code, out := s.run("nope\n", "approve"); code == 0 {
		t.Fatalf("approve with wrong confirmation should fail:\n%s", out)
	}
	if code, out := s.run("T-1\n", "approve"); code != 0 || !strings.Contains(out, "T-1 is now approved") {
		t.Fatalf("approve: %d\n%s", code, out)
	}

	// Editing the plan after approval voids it: back to planned, the old approval no longer counts.
	s.must("task", "move", "planned")
	s.write(".truehand/tasks/T-1/plan.md", validPlan+"\nextra\n")
	out = s.fails(3, "task", "move", "approved")
	if !strings.Contains(out, "changed after the last approval") {
		t.Fatalf("expected stale approval, got:\n%s", out)
	}
	s.write(".truehand/tasks/T-1/plan.md", validPlan)
	s.must("task", "move", "approved")

	// Cannot skip states.
	s.fails(1, "task", "move", "verified")
	t.Setenv("TRUEHAND_ACTOR", "agent:dev")
	s.must("task", "move", "building")

	// verified: the hand fails until the work exists, then passes.
	s.fails(3, "hand", "run", "check", "--quiet")
	s.fails(3, "task", "move", "verified")
	s.write("greet.txt", "hi\n")
	s.must("hand", "run", "check", "--quiet")
	out = s.must("task", "move", "verified")
	if !strings.Contains(out, "no changed file matches ui/**") {
		t.Fatalf("expected the ui rule to be skipped, got:\n%s", out)
	}

	// Changing content after verification makes evidence stale.
	s.write("greet.txt", "hello\n")
	out = s.fails(3, "gate", "check", "delivered")
	if !strings.Contains(out, "stale") {
		t.Fatalf("expected stale evidence, got:\n%s", out)
	}
	s.write("greet.txt", "hi\n")

	// reviewed: the developer cannot review their own work.
	notes := filepath.Join(t.TempDir(), "review.md")
	os.WriteFile(notes, []byte("Read greet.txt, matches the plan. No issues.\n"), 0o644)
	s.must("review", "--verdict", "pass", "--notes", notes)
	out = s.fails(3, "task", "move", "reviewed")
	if !strings.Contains(out, "who worked on this task") {
		t.Fatalf("expected self-review refusal, got:\n%s", out)
	}
	t.Setenv("TRUEHAND_ACTOR", "agent:reviewer")
	s.must("review", "--verdict", "pass", "--notes", notes)
	s.must("task", "move", "reviewed")

	// delivered: needs conventional commits; committing keeps evidence fresh.
	s.git("add", "greet.txt")
	s.git("commit", "-q", "-m", "add greeting")
	out = s.fails(3, "task", "move", "delivered")
	if !strings.Contains(out, "not a conventional commit") {
		t.Fatalf("expected conventional commit failure, got:\n%s", out)
	}
	s.git("commit", "-q", "--amend", "-m", "feat: add greeting")
	s.must("task", "move", "delivered")

	// Tampering with evidence is detected.
	idx := filepath.Join(s.dir, ".truehand/tasks/T-1/evidence")
	logs, _ := filepath.Glob(filepath.Join(idx, "check-*-output.log"))
	if len(logs) == 0 {
		t.Fatal("no check evidence written")
	}
	os.WriteFile(logs[len(logs)-1], []byte("forged\n"), 0o644)
	out = s.must("evidence", "list")
	if !strings.Contains(out, "TAMPERED") {
		t.Fatalf("expected tamper detection, got:\n%s", out)
	}

	var view core.TaskView
	if err := json.Unmarshal([]byte(s.must("task", "show", "--json")), &view); err != nil {
		t.Fatal(err)
	}
	if view.Task.State != "delivered" || view.Task.Roles["developer"] != "agent:dev" {
		t.Fatalf("unexpected final task: %+v", view.Task)
	}
}

func TestReworkIsUngated(t *testing.T) {
	s := newSandbox(t)
	s.must("init", "--stack", "generic")
	s.write(".truehand/config.yaml", "version: 0\ngates: {}\n")
	s.must("task", "new", "T-2", "--title", "x")
	s.must("task", "move", "approved")
	s.must("task", "move", "building")
	s.must("task", "move", "planned")
	if out := s.must("task", "list"); !strings.Contains(out, "planned") {
		t.Fatalf("expected planned, got %s", out)
	}
}

func TestUnknownCheckRejected(t *testing.T) {
	s := newSandbox(t)
	s.must("init", "--stack", "generic")
	s.write(".truehand/config.yaml", "version: 0\ngates:\n  verified:\n    - check: magic\n")
	code, out := s.run("", "task", "list")
	if code == 0 || !strings.Contains(out, `unknown check "magic"`) {
		t.Fatalf("expected config error, got %d:\n%s", code, out)
	}
}

func TestHandEvidenceGoesStale(t *testing.T) {
	s := newSandbox(t)
	s.must("init", "--stack", "generic")
	s.write(".truehand/config.yaml", "version: 0\nbase: main\ncommands:\n  check: \"test -f greet.txt\"\ngates:\n  verified:\n    - check: hand_passed\n      with: { hand: check }\n")
	s.must("task", "new", "T-3", "--title", "x")
	s.must("task", "move", "approved")
	s.must("task", "move", "building")
	s.write("greet.txt", "hi\n")
	s.must("hand", "run", "check", "--quiet")
	s.write("greet.txt", "changed\n")
	out := s.fails(3, "task", "move", "verified")
	if !strings.Contains(out, "is stale") || !strings.Contains(out, "greet.txt") {
		t.Fatalf("expected stale hand evidence naming greet.txt, got:\n%s", out)
	}
}

func TestHandThatChangesContentIsNotAPass(t *testing.T) {
	s := newSandbox(t)
	s.must("init", "--stack", "generic")
	s.write(".truehand/config.yaml", "version: 0\nbase: main\ngates: {}\n")
	s.must("task", "new", "T-4", "--title", "x")
	s.write("greet.txt", "hi\n")
	out := s.fails(3, "hand", "run", "fmt", "--quiet", "--", "echo more >> greet.txt")
	if !strings.Contains(out, "content changed while the hand ran") {
		t.Fatalf("expected content-changed error, got:\n%s", out)
	}
}

func TestPlanFetchFromFile(t *testing.T) {
	s := newSandbox(t)
	s.must("init", "--stack", "generic")
	s.write(".truehand/config.yaml", "version: 0\ntickets: { adapter: file }\ngates: {}\n")
	s.write("brief.md", "# Add dark mode\n\nUsers want dark mode.\n")
	s.must("task", "new", "T-5", "--title", "Dark mode", "--source", "file:brief.md")
	out := s.must("plan", "fetch")
	if !strings.Contains(out, "Add dark mode") {
		t.Fatalf("unexpected output:\n%s", out)
	}
	ctxmd := readFile(t, filepath.Join(s.dir, ".truehand/tasks/T-5/context.md"))
	if !strings.Contains(ctxmd, "Users want dark mode.") {
		t.Fatalf("context.md: %s", ctxmd)
	}
	if !strings.Contains(readFile(t, filepath.Join(s.dir, ".truehand/tasks/T-5/plan.md")), "## Verification") {
		t.Fatal("plan.md template not created")
	}
}

func TestGroupHelp(t *testing.T) {
	s := newSandbox(t)
	for _, args := range [][]string{{"task"}, {"task", "--help"}, {"hand", "-h"}} {
		out := s.must(args...)
		if !strings.Contains(out, args[0]+" ") || !strings.Contains(out, "Usage: truehand "+args[0]) {
			t.Fatalf("%v: %s", args, out)
		}
	}
}

func TestOwnTicketFlow(t *testing.T) {
	s := newSandbox(t)
	s.must("init", "--stack", "generic")
	out := s.must("task", "new", "--title", "Dark mode", "--description", "Users want a dark theme.\n\nFollow the system setting.")
	if !strings.Contains(out, "Created T-1") {
		t.Fatalf("expected auto id T-1:\n%s", out)
	}
	s.must("task", "new", "--title", "Second")
	if out := s.fails(1, "plan", "fetch", "--task", "T-2"); !strings.Contains(out, "has no description") {
		t.Fatalf("expected missing description error:\n%s", out)
	}
	s.must("task", "comment", "T-1", "Keep the toggle in settings too.")
	s.must("task", "edit", "T-1", "--title", "Dark theme")
	s.must("plan", "fetch", "--task", "T-1")
	ctxmd := readFile(t, filepath.Join(s.dir, ".truehand/tasks/T-1/context.md"))
	for _, want := range []string{"T-1 — Dark theme", "Follow the system setting.", "Keep the toggle in settings too."} {
		if !strings.Contains(ctxmd, want) {
			t.Fatalf("context.md lacks %q:\n%s", want, ctxmd)
		}
	}
	show := s.must("task", "show", "T-1", "--json")
	if !strings.Contains(show, `"description": "Users want a dark theme.`) {
		t.Fatalf("task show lacks the description:\n%s", show)
	}
	if out := s.fails(1, "task", "new", "--title", "x", "--source", "SPA-9"); out == "" {
		t.Fatal("expected an error")
	}
}

func TestAttachKeepsFilesForTheHumans(t *testing.T) {
	s := newSandbox(t)
	s.must("init", "--stack", "generic")
	s.must("task", "new", "--title", "Empty cart", "--description", "Show a message.")
	shot := filepath.Join(t.TempDir(), "empty.png")
	os.WriteFile(shot, []byte("\x89PNG fake"), 0o644)
	if out := s.fails(1, "hand", "attach", shot); !strings.Contains(out, "usage") {
		t.Fatalf("a note is required:\n%s", out)
	}
	out := s.must("hand", "attach", "--note", "The empty cart on the iPhone 17 Pro", shot, "--json")
	if !strings.Contains(out, `"verdict": "pass"`) || !strings.Contains(out, "attach-1-empty.png") {
		t.Fatalf("attach result:\n%s", out)
	}
	link := filepath.Join(t.TempDir(), "link.png")
	os.Symlink(shot, link)
	if out := s.fails(1, "hand", "attach", "--note", "x", link); !strings.Contains(out, "not a regular file") {
		t.Fatalf("links must be refused:\n%s", out)
	}
	if show := s.must("task", "show", "T-1", "--json"); !strings.Contains(show, `"hand": "attach"`) {
		t.Fatalf("the attachment is evidence of the task:\n%s", show)
	}
}
