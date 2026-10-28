package core

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/spaghetticoders/truehand/internal/board"
)

// One session reads many tasks: what it remembers for one must never answer for another.
func TestSessionKeepsTasksApart(t *testing.T) {
	dir, ws := repo(t)
	git(t, dir, "checkout", "-q", "-b", "dev")
	write(t, filepath.Join(dir, "dev.txt"), "dev\n")
	git(t, dir, "add", "dev.txt")
	git(t, dir, "commit", "-q", "-m", "feat: dev")
	git(t, dir, "checkout", "-q", "-b", "work")
	write(t, filepath.Join(dir, "a/x.go"), "a\n")
	write(t, filepath.Join(dir, "b/y.go"), "b\n")
	git(t, dir, "add", "a/x.go")
	git(t, dir, "commit", "-q", "-m", "feat: work")
	t1, _ := ws.NewTask("T-1", "main based", "", "a/**")
	t2, _ := ws.NewTask("T-2", "dev based", "", "b/**")
	t1.BaseRef = "main"
	t1.Base = git(t, dir, "rev-parse", "main")
	t2.BaseRef = "dev"
	// T-2 started on main but is measured against dev: its base is the merge-base with dev.
	t2.Base = git(t, dir, "rev-parse", "main")
	s := ws.session()
	for _, tk := range []*board.Task{t1, t2, t1} {
		want, _ := ws.BaseOf(tk)
		got, _ := s.BaseOf(tk)
		if got != want {
			t.Fatalf("%s: base %s, want %s", tk.ID, got, want)
		}
		wantFP, _ := ws.fingerprint(tk, want)
		gotFP, _ := s.fingerprint(tk, got)
		if !reflect.DeepEqual(gotFP, wantFP) {
			t.Fatalf("%s: fingerprint %v, want %v", tk.ID, gotFP.Files, wantFP.Files)
		}
	}
	b1, _ := s.BaseOf(t1)
	b2, _ := s.BaseOf(t2)
	if b1 == b2 {
		t.Fatal("setup: the two tasks should have different bases")
	}
	f1, _ := s.fingerprint(t1, b1)
	f2, _ := s.fingerprint(t2, b1)
	if reflect.DeepEqual(f1.Files, f2.Files) {
		t.Fatal("setup: different scopes should give different fingerprints")
	}
}
