package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spaghetticoders/truehand/internal/board"
)

func TestProjectBoard(t *testing.T) {
	dir, main := repo(t)
	if _, err := main.NewTask("M-1", "Main", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := main.NewTask("X-1", "Main copy", ""); err != nil {
		t.Fatal(err)
	}

	git(t, dir, "checkout", "-q", "-b", "feature")
	feature, _ := Open(dir)
	if _, err := feature.NewTask("F-1", "On a branch", ""); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".truehand/tasks/F-1")
	git(t, dir, "commit", "-q", "-m", "chore: f")
	git(t, dir, "checkout", "-q", "main")

	wts := t.TempDir()
	w1, t1, err := main.NewTaskWorktree(filepath.Join(wts, "T-1"), "", "Live", "")
	if err != nil {
		t.Fatal(err)
	}
	git(t, w1.Repo.Root, "add", "-A")
	git(t, w1.Repo.Root, "commit", "-q", "-m", "chore: t1")
	w1.Store.AppendEvent(t1.ID, board.Event{Type: "comment", Actor: "claude:dev", Detail: map[string]any{"text": "uncommitted, only the worktree has it: ok to go on?"}})

	gone, _, err := main.NewTaskWorktree(filepath.Join(wts, "T-2"), "", "Deleted by hand", "")
	if err != nil {
		t.Fatal(err)
	}
	git(t, gone.Repo.Root, "add", "-A")
	git(t, gone.Repo.Root, "commit", "-q", "-m", "chore: t2")
	if err := os.RemoveAll(gone.Repo.Root); err != nil {
		t.Fatal(err)
	}

	wx, err := CreateWorktree(main.Repo, "X-1", "main", filepath.Join(wts, "X-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wx.NewTask("X-1", "Worktree copy", ""); err != nil {
		t.Fatal(err)
	}

	pv, err := main.ProjectBoard()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ProjectTask{}
	var ids []string
	for _, pt := range pv.Tasks {
		if _, dup := got[pt.ID]; dup {
			t.Fatalf("%s twice", pt.ID)
		}
		got[pt.ID] = pt
		ids = append(ids, pt.ID)
	}
	if len(ids) != 5 {
		t.Fatalf("tasks %v", ids)
	}
	if pt := got["T-1"]; !sameDir(pt.Worktree, w1.Repo.Root) || pt.Branch != "" || !pt.Active {
		t.Fatalf("T-1 %+v", pt)
	}
	if pt := got["F-1"]; pt.Branch != "feature" || pt.Worktree != "" {
		t.Fatalf("F-1 %+v", pt)
	}
	if pt := got["T-2"]; pt.Branch != "th/T-2" {
		t.Fatalf("task of a hand-deleted worktree must stay visible from its branch: %+v", pt)
	}
	if pt := got["X-1"]; pt.Title != "Worktree copy" || pt.Worktree == "" {
		t.Fatalf("worktree copy must win: %+v", pt)
	}
	if pt := got["M-1"]; pt.Worktree != "" || pt.Branch != "" {
		t.Fatalf("M-1 %+v", pt)
	}
	if len(pv.Waiting) != 5 || len(pv.WorktreeErrors) != 0 {
		t.Fatalf("waiting %d errors %+v", len(pv.Waiting), pv.WorktreeErrors)
	}
	for _, in := range pv.Waiting {
		if in.Task.ID == "T-1" && len(in.Events) != 2 {
			t.Fatalf("T-1 waiting input not read live: %+v", in.Events)
		}
	}
	found := false
	for _, it := range WaitingFor(pv.Waiting, nil) {
		found = found || it.Task == "T-1" && it.Kind == WaitCommentQuestion
	}
	if !found {
		t.Fatal("the worktree task's comment is not in the waiting list")
	}
}
