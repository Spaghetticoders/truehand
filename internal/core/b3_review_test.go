package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/config"
	"github.com/spaghetticoders/truehand/internal/gitx"
)

func gitRepo(t *testing.T, dir string) *gitx.Repo {
	t.Helper()
	r, err := gitx.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestReviewInitConfigRefusesBaseThatBreaksYAML(t *testing.T) {
	dir := bareRepo(t)
	r := gitRepo(t, dir)
	for _, base := range []string{"main\ngates: {}", "main: x", "-main"} {
		_, err := InitConfig(r, "go", base, true)
		if err == nil {
			b, _ := os.ReadFile(config.Path(dir))
			if verr := ValidateConfig(b); verr != nil {
				t.Errorf("base %q: InitConfig wrote a config Open refuses: %v", base, verr)
			} else {
				t.Errorf("base %q accepted", base)
			}
		}
	}
}

func TestReviewWaitingMergedNeverWaits(t *testing.T) {
	m := task("M", board.Delivered)
	m.Merged = true
	m.Stale = true
	in := WaitingInput{Task: m, Events: []board.Event{{Type: "gate_closed", Time: at(1)}}}
	sessions := []SessionState{{ID: "s1", Task: "M", Role: "dev", Status: SessionCrashed, Since: at(3)}}
	if got := kinds(WaitingFor([]WaitingInput{in}, sessions)); len(got) != 0 {
		t.Fatalf("merged task still waits: %v", got)
	}
}

func TestReviewWaitingMergedHasNoOpenQuestion(t *testing.T) {
	m := task("M", board.Delivered)
	m.Merged = true
	in := WaitingInput{Task: m, Events: []board.Event{{Type: "comment", Actor: "claude:s1", Time: at(2)}}}
	if got := kinds(WaitingFor([]WaitingInput{in}, nil)); len(got) != 0 {
		t.Fatalf("merged task waits for an answer forever: %v", got)
	}
}

func TestReviewWaitingStaleNotMerged(t *testing.T) {
	s := task("S", board.Reviewed)
	s.Stale = true
	got := kinds(WaitingFor([]WaitingInput{{Task: s}}, nil))
	if strings.Join(got, ",") != "S:stale" {
		t.Fatalf("got %v", got)
	}
}

func TestReviewWaitingBlockedOnlyLatestSession(t *testing.T) {
	v := task("B", board.Building)
	sessions := []SessionState{
		{ID: "old", Task: "B", Status: SessionExited, ExitCode: code(1), Since: at(1)},
		{ID: "new", Task: "B", Status: SessionWorking, Since: at(2)},
	}
	if got := kinds(WaitingFor([]WaitingInput{{Task: v}}, sessions)); len(got) != 0 {
		t.Fatalf("a failed session replaced by a live one still blocks: %v", got)
	}
	sessions[1].Status = SessionExited
	sessions[1].ExitCode = code(0)
	if got := kinds(WaitingFor([]WaitingInput{{Task: v}}, sessions)); len(got) != 0 {
		t.Fatalf("clean exit blocks: %v", got)
	}
}

func TestReviewCreateWorktreeRefusesBadRefIDsCleanly(t *testing.T) {
	dir, ws := repo(t)
	for _, id := range []string{"a..b", "x.lock", "ends."} {
		path := filepath.Join(t.TempDir(), "wt", id)
		if _, err := CreateWorktree(ws.Repo, id, "main", path); err == nil {
			t.Errorf("id %q accepted", id)
		}
		if _, err := os.Lstat(path); err == nil {
			t.Errorf("id %q left %s", id, path)
		}
	}
	for _, id := range []string{"../x", "-b", "a/b", ""} {
		if _, err := CreateWorktree(ws.Repo, id, "main", filepath.Join(t.TempDir(), "x")); err == nil {
			t.Errorf("id %q accepted", id)
		}
	}
	if out := git(t, dir, "branch", "--list", "th/*"); out != "" {
		t.Fatalf("branches left: %q", out)
	}
	if n := len(strings.Split(git(t, dir, "worktree", "list"), "\n")); n != 1 {
		t.Fatalf("worktrees left: %d", n)
	}
}

func TestReviewCreateWorktreeRejectsOptionBase(t *testing.T) {
	_, ws := repo(t)
	for _, base := range []string{"--orphan", "-", ""} {
		if _, err := CreateWorktree(ws.Repo, "T-1", base, filepath.Join(t.TempDir(), "w")); err == nil {
			t.Errorf("base %q accepted", base)
		}
	}
}

func TestReviewNewTaskWorktreeMissingRemoteBase(t *testing.T) {
	dir := bareRepo(t)
	write(t, filepath.Join(dir, ".truehand/config.yaml"), strings.Replace(testConfig, "base: main", "base: origin/main", 1))
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "chore: truehand")
	ws, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "wt", "T-1")
	if _, _, err := ws.NewTaskWorktree(path, "", "x", ""); err == nil {
		t.Fatal("worktree made from a base that does not exist")
	}
	if _, err := os.Lstat(path); err == nil {
		t.Fatal("worktree dir left")
	}
	if out := git(t, dir, "branch", "--list", "th/*"); out != "" {
		t.Fatalf("branch left: %q", out)
	}
}

func TestReviewNewTaskWorktreeRollsBackOnTaskError(t *testing.T) {
	dir, ws := repo(t)
	path := filepath.Join(t.TempDir(), "wt", "T-1")
	if _, _, err := ws.NewTaskWorktree(path, "T-1", "x", "no-tracker-ref"); err == nil {
		t.Fatal("bad source accepted")
	}
	if _, err := os.Lstat(path); err == nil {
		t.Fatal("worktree dir left after a failed task")
	}
	if out := git(t, dir, "branch", "--list", "th/*"); out != "" {
		t.Fatalf("branch left: %q", out)
	}
	if _, _, err := ws.NewTaskWorktree(path, "T-1", "x", ""); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
}

func TestReviewWorktreeActiveTaskIsPerWorktree(t *testing.T) {
	_, ws := repo(t)
	if err := ws.SetActiveTask("MAIN"); err != nil {
		t.Fatal(err)
	}
	w1, _, err := ws.NewTaskWorktree(filepath.Join(t.TempDir(), "T-1"), "", "one", "")
	if err != nil {
		t.Fatal(err)
	}
	w2, _, err := ws.NewTaskWorktree(filepath.Join(t.TempDir(), "T-2"), "", "two", "")
	if err != nil {
		t.Fatal(err)
	}
	if a, b, c := ws.ActiveTask(), w1.ActiveTask(), w2.ActiveTask(); a != "MAIN" || b != "T-1" || c != "T-2" {
		t.Fatalf("active: main=%q w1=%q w2=%q", a, b, c)
	}
}

func TestReviewRemoveWorktreeKeepsMainAndUnmergedBranch(t *testing.T) {
	dir, ws := repo(t)
	if err := RemoveWorktree(ws.Repo, dir, true, true); err == nil {
		t.Fatal("removed the main checkout")
	}
	path := filepath.Join(t.TempDir(), "T-1")
	w1, _, err := ws.NewTaskWorktree(path, "", "one", "")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(path, "a.txt"), "a\n")
	git(t, path, "add", "-A")
	git(t, path, "commit", "-q", "-m", "feat: a")
	_ = w1
	err = RemoveWorktree(ws.Repo, path, true, false)
	if err == nil {
		t.Fatal("unmerged branch deleted without force")
	}
	if _, serr := os.Lstat(path); serr == nil {
		t.Fatalf("worktree still there, so the branch was never tried: %v", err)
	}
	if out := git(t, dir, "branch", "--list", "th/T-1"); out == "" {
		t.Fatal("unmerged branch gone without force")
	}
}

func TestReviewDiffUntrackedSymlinkDoesNotLeakTarget(t *testing.T) {
	_, ws := repo(t)
	path := filepath.Join(t.TempDir(), "T-1")
	w, tk, err := ws.NewTaskWorktree(path, "", "one", "")
	if err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "secret.txt")
	write(t, secret, "TOP-SECRET-CONTENT\n")
	if err := os.Symlink(secret, filepath.Join(path, "link")); err != nil {
		t.Fatal(err)
	}
	d, err := w.Diff(tk, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(d.Patch, "TOP-SECRET-CONTENT") {
		t.Fatalf("diff follows an untracked symlink out of the worktree:\n%s", d.Patch)
	}
	if d2, err := w.Diff(tk, "link", 0); err == nil && strings.Contains(d2.Patch, "TOP-SECRET-CONTENT") {
		t.Fatal("file diff follows the symlink")
	}
}

func TestReviewDiffOptionLikeFileNames(t *testing.T) {
	_, ws := repo(t)
	path := filepath.Join(t.TempDir(), "T-1")
	w, tk, err := ws.NewTaskWorktree(path, "", "one", "")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(path, "--output=pwned"), "x\n")
	write(t, filepath.Join(path, "-R"), "y\n")
	d, err := w.Diff(tk, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Files) != 2 {
		t.Fatalf("files: %+v", d.Files)
	}
	for _, f := range []string{"--output=pwned", "-R"} {
		if _, err := w.Diff(tk, f, 0); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(path, "pwned")); err == nil {
		t.Fatal("a file name was read as a git option")
	}
	if _, err := w.Diff(tk, "../../etc/passwd", 0); err == nil {
		t.Fatal("file outside the diff accepted")
	}
}

func TestReviewDiffExcludesTruehandAndCountsWorktreeChanges(t *testing.T) {
	_, ws := repo(t)
	path := filepath.Join(t.TempDir(), "T-1")
	w, tk, err := ws.NewTaskWorktree(path, "", "one", "")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(path, "a.go"), "package a\n")
	git(t, path, "add", "-A")
	git(t, path, "commit", "-q", "-m", "feat: a")
	write(t, filepath.Join(path, "a.go"), "package a\n\nvar X = 1\n")
	d, err := w.Diff(tk, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Files) != 1 || d.Files[0].Path != "a.go" || d.Files[0].Status != "A" || d.Files[0].Add != 3 {
		t.Fatalf("files: %+v", d.Files)
	}
}

func TestReviewDiffTruncation(t *testing.T) {
	if s, cut := cut("abc\ndef\n", 6); s != "abc\n" || !cut {
		t.Fatalf("%q %v", s, cut)
	}
	if s, cut := cut("abc\n", 4); s != "abc\n" || cut {
		t.Fatalf("%q %v", s, cut)
	}
}

func TestReviewActivateKeepsForeignHook(t *testing.T) {
	dir := bareRepo(t)
	r := gitRepo(t, dir)
	hook := filepath.Join(dir, ".git/hooks/pre-push")
	write(t, hook, "#!/bin/sh\necho mine\n")
	if _, err := Activate(r, ActivateOptions{}); err == nil {
		t.Fatal("foreign hook replaced without force")
	}
	if b, _ := os.ReadFile(hook); string(b) != "#!/bin/sh\necho mine\n" {
		t.Fatalf("foreign hook changed: %q", b)
	}
	st, err := GitHookStates(r)
	if err != nil {
		t.Fatal(err)
	}
	if st["pre-push"] != HookForeign {
		t.Fatalf("states %v", st)
	}
}

func TestReviewGitHookStatesSharedInWorktree(t *testing.T) {
	dir, ws := repo(t)
	git(t, dir, "config", "--unset", "core.hooksPath")
	if _, err := InstallGitHooks(ws.Repo, false); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "T-1")
	w, _, err := ws.NewTaskWorktree(path, "", "one", "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := GitHookStates(w.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if st["pre-push"] != HookOurs || st["commit-msg"] != HookOurs {
		t.Fatalf("worktree sees %v (main %s)", st, dir)
	}
}

func TestReviewEditRejectsBadID(t *testing.T) {
	_, ws := repo(t)
	title := "x"
	for _, id := range []string{"../config", "/etc", ""} {
		if _, _, err := ws.Edit(id, TaskEdit{Title: &title}); err == nil {
			t.Errorf("id %q accepted", id)
		}
	}
}

func TestReviewEditMakesEditorAWorker(t *testing.T) {
	_, ws := repo(t)
	tk, err := ws.NewTask("T-1", "x", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRUEHAND_ACTOR", "claude:editor")
	desc := "new"
	if _, _, err := ws.Edit(tk.ID, TaskEdit{Description: &desc}); err != nil {
		t.Fatal(err)
	}
	w, err := ws.Workers(tk)
	if err != nil {
		t.Fatal(err)
	}
	if !w["claude:editor"] {
		t.Fatalf("editor not a worker: %v", w)
	}
}

func TestReviewEditNoChangeLogsNothing(t *testing.T) {
	_, ws := repo(t)
	tk, err := ws.NewTask("T-1", "same", "")
	if err != nil {
		t.Fatal(err)
	}
	title := "same"
	_, changed, err := ws.Edit(tk.ID, TaskEdit{Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	evs, _ := ws.Store.Events(tk.ID)
	for _, e := range evs {
		if e.Type == "edited" {
			t.Fatalf("unchanged title logged an edit (%v): a no-op edit would bar the human from review", changed)
		}
	}
}

func TestReviewResolveActorsUnknownSession(t *testing.T) {
	got := ResolveActors([]string{"claude:0123456789abcdef", "human:", "weird"}, nil, Identity{})
	if a := got["claude:0123456789abcdef"]; a.Kind != ActorAgent || a.Role != "" {
		t.Fatalf("%+v", a)
	}
	if a := got["human:"]; a.Kind != ActorHuman {
		t.Fatalf("%+v", a)
	}
	if got["weird"].Kind != ActorUnknown {
		t.Fatalf("%+v", got["weird"])
	}
}

func TestReviewWorktreeTaskShownOnceAcrossCheckouts(t *testing.T) {
	_, ws := repo(t)
	path := filepath.Join(t.TempDir(), "T-1")
	w, tk, err := ws.NewTaskWorktree(path, "", "one", "")
	if err != nil {
		t.Fatal(err)
	}
	git(t, path, "add", "-A")
	git(t, path, "commit", "-q", "-m", "chore: task")
	mainBoard, err := ws.Board()
	if err != nil {
		t.Fatal(err)
	}
	wtBoard, err := w.Board()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, bv := range []*BoardView{mainBoard, wtBoard} {
		for _, bt := range bv.Tasks {
			if bt.ID == tk.ID {
				n++
			}
		}
	}
	if n != 1 {
		t.Fatalf("task %s appears %d times across the main board and its worktree's board: the waiting list and the board would count it twice", tk.ID, n)
	}
}
