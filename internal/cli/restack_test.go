package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/evidence"
	"github.com/spaghetticoders/truehand/internal/hands"
	"github.com/spaghetticoders/truehand/internal/humanauth"
)

// stackSandbox: COACH-1 reviewed on feat with passing evidence, COACH-2 made the old way
// (from main) with a commit of its own.
func stackSandbox(t *testing.T) (*sandbox, string) {
	s := newSandbox(t)
	s.write(".truehand/config.yaml", "version: 0\nbase: main\ncommands:\n  review: \"true\"\ngates:\n  verified:\n    - check: hand_passed\n      with: { hand: review }\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "chore: truehand")
	main := strings.TrimSpace(s.git("rev-parse", "HEAD"))
	s.git("checkout", "-q", "-b", "feat")
	ws, err := core.Open(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	c1, _ := ws.NewTask("COACH-1", "first", "")
	s.write("a.txt", "one\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "feat: one")
	sess, err := hands.Begin(ws, c1, "review", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Finish(evidence.Pass, "ok", ""); err != nil {
		t.Fatal(err)
	}
	c1.State = board.Reviewed
	ws.Store.Save(c1)
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "chore: reviewed")
	tip := strings.TrimSpace(s.git("rev-parse", "HEAD"))
	ws.Store.Create(&board.Task{ID: "COACH-2", Title: "second", State: board.Building, Base: main, BaseRef: "main"})
	s.write("b.txt", "two\n")
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "feat: two")
	return s, tip
}

func TestTaskRestackHumansOnly(t *testing.T) {
	s, tip := stackSandbox(t)
	withTTY(t, true)
	t.Setenv("TRUEHAND_ACTOR", "agent:dev")
	if out := s.fails(1, "task", "restack", "COACH-2"); !strings.Contains(out, "Agents cannot restack") {
		t.Fatalf("agent: %s", out)
	}
	t.Setenv("TRUEHAND_ACTOR", "")
	withTTY(t, false)
	if out := s.fails(1, "task", "restack", "COACH-2"); !strings.Contains(out, "not a TTY") {
		t.Fatalf("no tty: %s", out)
	}
	withTTY(t, true)
	asked := 0
	old := humanauth.Authenticate
	humanauth.Authenticate = func(string) error { asked++; return nil }
	t.Cleanup(func() { humanauth.Authenticate = old })
	if code, out := s.run("nope\n", "task", "restack", "COACH-2"); code == 0 || !strings.Contains(out, "not restacked") || asked != 0 {
		t.Fatalf("wrong confirmation: %d %s", code, out)
	}
	code, out := s.run("COACH-2\n", "task", "restack", "COACH-2")
	if code != 0 || asked != 1 || !strings.Contains(out, "Restacked: COACH-2 now starts at "+tip[:12]+", on COACH-1") {
		t.Fatalf("restack: %d %s", code, out)
	}
	b, _ := os.ReadFile(filepath.Join(s.dir, ".truehand/tasks/COACH-2/task.yaml"))
	if !strings.Contains(string(b), "stacked_on: COACH-1") || !strings.Contains(string(b), "base: "+tip) {
		t.Fatalf("task.yaml: %s", b)
	}
}
