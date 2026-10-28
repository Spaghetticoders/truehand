package core

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spaghetticoders/truehand/internal/config"
)

// withOrigin makes dir a clone-like repo: an origin with main, and base origin/main.
func withOrigin(t *testing.T, dir string) {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "origin.git")
	git(t, dir, "init", "-q", "--bare", remote)
	git(t, dir, "remote", "add", "origin", remote)
	git(t, dir, "push", "-q", "-u", "origin", "main")
}

func TestNewTaskWorktree(t *testing.T) {
	dir, _ := repo(t)
	write(t, config.Path(dir), strings.Replace(testConfig, "base: main", "base: origin/main", 1))
	git(t, dir, "commit", "-q", "-am", "chore: base origin/main")
	withOrigin(t, dir)
	main, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := main.SetActiveTask("MAIN-1"); err != nil {
		t.Fatal(err)
	}
	wts := filepath.Join(t.TempDir(), "worktrees", "p_1")

	ws1, t1, err := main.NewTaskWorktree(filepath.Join(wts, "T-1"), "", "First", "")
	if err != nil {
		t.Fatal(err)
	}
	ws2, t2, err := main.NewTaskWorktree(filepath.Join(wts, "T-2"), "", "Second", "")
	if err != nil {
		t.Fatal(err)
	}
	if t1.ID != "T-1" || t2.ID != "T-2" {
		t.Fatalf("ids %s %s: worktrees must not reuse ids", t1.ID, t2.ID)
	}
	if t1.BaseRef != "origin/main" || t1.Base != git(t, dir, "rev-parse", "origin/main") {
		t.Fatalf("task base %s %s", t1.BaseRef, t1.Base)
	}

	if a := main.ActiveTask(); a != "MAIN-1" {
		t.Fatalf("main checkout's active task became %q", a)
	}
	if a, b := ws1.ActiveTask(), ws2.ActiveTask(); a != "T-1" || b != "T-2" {
		t.Fatalf("worktree active tasks %q %q", a, b)
	}
	if _, err := main.Store.Load("T-1"); err == nil {
		t.Fatal("task created in the main checkout")
	}
	if br := git(t, ws1.Repo.Root, "branch", "--show-current"); br != "th/T-1" {
		t.Fatalf("worktree on %s", br)
	}
	if _, err := ws1.Repo.Git("rev-parse", "--verify", "-q", "th/T-1@{upstream}"); err == nil {
		t.Fatal("task branch tracks the base: a plain push would go to it")
	}

	list, err := ListWorktrees(main.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || !list[0].Main || list[0].Branch != "main" || list[1].Task != "T-1" || list[2].Task != "T-2" || list[1].Main {
		t.Fatalf("worktrees %+v", list)
	}
	if !sameDir(list[1].Path, ws1.Repo.Root) {
		t.Fatalf("path %s vs %s", list[1].Path, ws1.Repo.Root)
	}

	if _, _, err := main.NewTaskWorktree(filepath.Join(wts, "again"), "T-1", "Dup", ""); err == nil {
		t.Fatal("duplicate id accepted")
	}
	if _, err := main.NewTask("LOCAL-1", "In the main checkout", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := main.NewTaskWorktree(filepath.Join(wts, "local"), "LOCAL-1", "Dup", ""); err == nil {
		t.Fatal("id of a task in the main checkout accepted")
	}
	if _, _, err := main.NewTaskWorktree(filepath.Join(wts, "bad"), "", "Bad", "SPA-1"); err == nil {
		t.Fatal("ticket without tracker accepted")
	}
	if _, err := os.Stat(filepath.Join(wts, "bad")); !os.IsNotExist(err) {
		t.Fatal("failed task left its worktree behind")
	}
	if _, err := main.Repo.Git("rev-parse", "--verify", "-q", "refs/heads/th/T-3"); err == nil {
		t.Fatal("failed task left its branch behind")
	}

	if err := RemoveWorktree(main.Repo, dir, true, true); err == nil {
		t.Fatal("main checkout removed")
	}
	write(t, filepath.Join(ws2.Repo.Root, "wip.txt"), "x")
	if err := RemoveWorktree(main.Repo, ws2.Repo.Root, true, false); err == nil {
		t.Fatal("dirty worktree removed without force")
	}
	if err := RemoveWorktree(main.Repo, ws2.Repo.Root, true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := main.Repo.Git("rev-parse", "--verify", "-q", "refs/heads/th/T-2"); err == nil {
		t.Fatal("branch kept")
	}
	if id, _ := main.NextTaskID(); id != "T-2" {
		t.Fatalf("next id after removing T-2 is %s", id)
	}
}

func TestCreateWorktreeRefuses(t *testing.T) {
	dir := bareRepo(t)
	write(t, config.Path(dir), testConfig)
	ws, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(t.TempDir(), "wt")
	if _, err := CreateWorktree(ws.Repo, "T-1", "main", wt); err == nil || !strings.Contains(err.Error(), "not committed") {
		t.Fatalf("worktree without a committed config: %v", err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "chore: truehand")
	for _, c := range []struct{ id, base, path string }{
		{"../x", "main", wt},
		{"T-1", "nope", wt},
		{"T-1", "--orphan", wt},
		{"T-1", "main", "relative/wt"},
		{"T-1", "main", dir},
	} {
		if _, err := CreateWorktree(ws.Repo, c.id, c.base, c.path); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
	got, err := CreateWorktree(ws.Repo, "T-1", "main", wt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := got.NewTask("T-1", "x", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenWorktree(filepath.Join(wt, ".truehand")); err == nil {
		t.Fatal("subdirectory opened as a worktree")
	}
	if _, err := CreateWorktree(ws.Repo, "T-1", "main", wt+"2"); err == nil {
		t.Fatal("second worktree on the same task branch")
	}
}

func TestNextTaskIDSeesBranches(t *testing.T) {
	dir, ws := repo(t)
	git(t, dir, "branch", "th/T-7")
	git(t, dir, "checkout", "-q", "-b", "feature")
	other, _ := Open(dir)
	if _, err := other.NewTask("T-9", "x", ""); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "chore: task")
	git(t, dir, "checkout", "-q", "main")
	if _, err := ws.Store.Load("T-9"); err == nil {
		t.Fatal("setup: T-9 should live only on feature")
	}
	if id, _ := ws.NextTaskID(); id != "T-10" {
		t.Fatalf("next id %s", id)
	}
}

func TestWorktreeErrorsAreRecognisable(t *testing.T) {
	_, ws := repo(t)
	for _, id := range []string{"a..b", "x.lock", "ends."} {
		_, err := CreateWorktree(ws.Repo, id, "main", filepath.Join(t.TempDir(), "w"))
		if !errors.Is(err, ErrBadBranchName) || !strings.Contains(err.Error(), id) {
			t.Errorf("id %q: %v", id, err)
		}
	}

	path := filepath.Join(t.TempDir(), "T-1")
	w, _, err := ws.NewTaskWorktree(path, "", "x", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := RemoveWorktree(ws.Repo, path, true, false); !errors.Is(err, ErrWorktreeDirty) {
		t.Fatalf("uncommitted task: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("dirty worktree removed")
	}
	git(t, path, "add", "-A")
	git(t, path, "commit", "-q", "-m", "chore: task")
	if err := RemoveWorktree(ws.Repo, path, true, false); !errors.Is(err, ErrBranchNotMerged) {
		t.Fatalf("unmerged branch: %v", err)
	}
	if out := git(t, ws.Repo.Root, "branch", "--list", "th/T-1"); out == "" {
		t.Fatal("unmerged branch deleted")
	}
	if err := RemoveWorktree(ws.Repo, w.Repo.Root, false, false); err == nil {
		t.Fatal("removing a worktree that is gone succeeded")
	}

	path2 := filepath.Join(t.TempDir(), "T-2")
	if _, _, err := ws.NewTaskWorktree(path2, "", "y", ""); err != nil {
		t.Fatal(err)
	}
	git(t, path2, "add", "-A")
	git(t, path2, "commit", "-q", "-m", "chore: task")
	git(t, ws.Repo.Root, "merge", "-q", "--ff-only", "th/T-2")
	if err := RemoveWorktree(ws.Repo, path2, true, false); err != nil {
		t.Fatalf("merged and clean: %v", err)
	}
	if out := git(t, ws.Repo.Root, "branch", "--list", "th/T-2"); out != "" {
		t.Fatal("merged branch kept")
	}
}

func TestRemoveWorktreePushedBranch(t *testing.T) {
	dir, ws := repo(t)
	withOrigin(t, dir)
	path := filepath.Join(t.TempDir(), "T-1")
	if _, _, err := ws.NewTaskWorktree(path, "", "x", ""); err != nil {
		t.Fatal(err)
	}
	git(t, path, "add", "-A")
	git(t, path, "commit", "-q", "-m", "chore: task")
	git(t, path, "push", "-q", "-u", "origin", "th/T-1")
	if err := RemoveWorktree(ws.Repo, path, true, false); err != nil {
		t.Fatalf("branch safe on its upstream: %v", err)
	}
}
