package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/config"
	"github.com/spaghetticoders/truehand/internal/gitx"
)

// A task built by an agent gets its own worktree on its own branch, so several agents can
// work on one repo at once and each worktree has its own active task (it has its own git dir).

const TaskBranchPrefix = "th/"

func TaskBranch(id string) string { return TaskBranchPrefix + id }

type Worktree struct {
	Path   string `json:"path"`
	Head   string `json:"head,omitempty"`
	Branch string `json:"branch,omitempty"`
	// Main is the repo's own checkout; the others were added with `git worktree add`.
	Main     bool `json:"main"`
	Detached bool `json:"detached,omitempty"`
	// Task is the id when Branch is a task branch (th/<id>).
	Task string `json:"task,omitempty"`
}

// ListWorktrees lists the repo's checkouts, the main one first.
func ListWorktrees(repo *gitx.Repo) ([]Worktree, error) {
	out, err := repo.Git("worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var list []Worktree
	for _, block := range strings.Split(strings.TrimSpace(out), "\n\n") {
		var wt Worktree
		bare := false
		for _, line := range strings.Split(block, "\n") {
			key, val, _ := strings.Cut(line, " ")
			switch key {
			case "worktree":
				wt.Path = val
			case "HEAD":
				wt.Head = val
			case "branch":
				wt.Branch = strings.TrimPrefix(val, "refs/heads/")
			case "detached":
				wt.Detached = true
			case "bare":
				bare = true
			}
		}
		if wt.Path == "" || bare {
			continue
		}
		wt.Main = len(list) == 0
		if id, ok := strings.CutPrefix(wt.Branch, TaskBranchPrefix); ok {
			wt.Task = id
		}
		list = append(list, wt)
	}
	return list, nil
}

// CreateWorktree checks out a new branch th/<id>, started at base, in a new worktree at path.
// The branch does not track base: pushing it must create th/<id> on the remote, never update
// the base branch. base must contain a committed .truehand/config.yaml.
func CreateWorktree(repo *gitx.Repo, id, base, path string) (*Workspace, error) {
	if err := board.ValidID(id); err != nil {
		return nil, err
	}
	if _, err := repo.Git("check-ref-format", "refs/heads/"+TaskBranch(id)); err != nil {
		return nil, fmt.Errorf("task id %q: %w", id, ErrBadBranchName)
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("worktree path %q is not absolute", path)
	}
	if _, err := os.Lstat(path); err == nil {
		return nil, fmt.Errorf("%s already exists", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if base == "" || strings.HasPrefix(base, "-") {
		return nil, fmt.Errorf("invalid base %q", base)
	}
	commit, err := repo.Git("rev-parse", "--verify", "-q", base+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("base %q is not a commit in this repo", base)
	}
	commit = strings.TrimSpace(commit)
	cfg := filepath.ToSlash(filepath.Join(config.Dir, config.File))
	if _, err := repo.Git("cat-file", "-e", commit+":"+cfg); err != nil {
		return nil, fmt.Errorf("%s is not committed on %s: commit it first, the task's worktree starts from there", cfg, base)
	}
	branch := TaskBranch(id)
	if _, err := repo.Git("rev-parse", "--verify", "-q", "refs/heads/"+branch); err == nil {
		return nil, fmt.Errorf("branch %s already exists", branch)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if _, err := repo.Git("worktree", "add", "--no-track", "-b", branch, path, commit); err != nil {
		return nil, err
	}
	return OpenWorktree(path)
}

// OpenWorktree opens the workspace of a worktree, refusing a path inside one instead of its root.
func OpenWorktree(path string) (*Workspace, error) {
	ws, err := Open(path)
	if err != nil {
		return nil, err
	}
	want, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	got, err := filepath.EvalSymlinks(ws.Repo.Root)
	if err != nil {
		return nil, err
	}
	if want != got {
		return nil, fmt.Errorf("%s is not the root of a worktree (that is %s)", path, ws.Repo.Root)
	}
	return ws, nil
}

// ErrBadBranchName: the task id is valid but cannot name a git branch (th/<id>).
var ErrBadBranchName = errors.New(`cannot name a git branch (no "..", no trailing "." or ".lock")`)

// Errors RemoveWorktree returns without force. ErrBranchNotMerged comes after the worktree,
// which holds nothing uncommitted, was removed; the branch is kept.
var (
	ErrWorktreeDirty   = errors.New("the worktree has uncommitted changes")
	ErrBranchNotMerged = errors.New("the task branch is not merged")
)

// RemoveWorktree removes a task's worktree, and its branch when deleteBranch is set. Without
// force it never loses work: a dirty worktree (a task not yet committed is dirty) is kept,
// and so is a branch not merged.
func RemoveWorktree(repo *gitx.Repo, path string, deleteBranch, force bool) error {
	var branch string
	found := false
	list, err := ListWorktrees(repo)
	if err != nil {
		return err
	}
	for _, wt := range list {
		if sameDir(wt.Path, path) {
			if wt.Main {
				return errors.New("the main checkout is not a task worktree")
			}
			branch, found = wt.Branch, true
		}
	}
	if !found {
		return fmt.Errorf("%s is not a worktree of %s", path, repo.Root)
	}
	deleteBranch = deleteBranch && strings.HasPrefix(branch, TaskBranchPrefix)
	if !force {
		wt := &gitx.Repo{Root: path}
		if out, err := wt.Git("status", "--porcelain", "--untracked-files=all"); err != nil {
			return err
		} else if strings.TrimSpace(out) != "" {
			return fmt.Errorf("%s: %w", path, ErrWorktreeDirty)
		}
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	if _, err := repo.Git(append(args, path)...); err != nil {
		return err
	}
	if !deleteBranch {
		return nil
	}
	if !force && !merged(repo, branch) {
		return fmt.Errorf("worktree removed, branch %s kept: %w", branch, ErrBranchNotMerged)
	}
	flag := "-d"
	if force {
		flag = "-D"
	}
	_, err = repo.Git("branch", flag, branch)
	return err
}

// merged mirrors `git branch -d`: the branch is in its upstream, or in HEAD without one.
func merged(repo *gitx.Repo, branch string) bool {
	into := "HEAD"
	if _, err := repo.Git("rev-parse", "--verify", "-q", branch+"@{upstream}"); err == nil {
		into = branch + "@{upstream}"
	}
	_, err := repo.Git("merge-base", "--is-ancestor", "refs/heads/"+branch, into)
	return err == nil
}

func sameDir(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	if err1 != nil || err2 != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

// taskIDs are the ids in use across the repo's checkouts, task branches and other local
// branches, so tasks created in parallel worktrees never collide.
func (w *Workspace) taskIDs() (map[string]bool, error) {
	ids := map[string]bool{}
	list, err := ListWorktrees(w.Repo)
	if err != nil {
		return nil, err
	}
	dirs := []string{w.Store.TasksDir()}
	for _, wt := range list {
		dirs = append(dirs, (&board.Store{Root: wt.Path}).TasksDir())
		if wt.Task != "" {
			ids[wt.Task] = true
		}
	}
	for _, d := range dirs {
		entries, _ := os.ReadDir(d)
		for _, e := range entries {
			if e.IsDir() {
				ids[e.Name()] = true
			}
		}
	}
	if out, err := w.Repo.Git("for-each-ref", "--format=%(refname:short)", "refs/heads/"+TaskBranchPrefix); err == nil {
		for _, b := range strings.Fields(out) {
			ids[strings.TrimPrefix(b, TaskBranchPrefix)] = true
		}
	}
	for _, bt := range w.branchTasks(map[string]bool{}) {
		ids[bt.ID] = true
	}
	return ids, nil
}

// NextTaskID is the next free id with the config's prefix, across all checkouts and branches.
func (w *Workspace) NextTaskID() (string, error) {
	ids, err := w.taskIDs()
	if err != nil {
		return "", err
	}
	return nextID(ids, w.Config.TaskPrefix()), nil
}

func nextID(ids map[string]bool, prefix string) string {
	n := 0
	for id := range ids {
		rest, ok := strings.CutPrefix(id, prefix+"-")
		if !ok {
			continue
		}
		var k int
		if _, err := fmt.Sscanf(rest, "%d", &k); err == nil && fmt.Sprint(k) == rest && k > n {
			n = k
		}
	}
	return fmt.Sprintf("%s-%d", prefix, n+1)
}

// NewTaskWorktree creates a task in its own worktree at path: branch th/<id> from the
// config's base, the task created there and made that worktree's active task. An empty id
// takes the next free one.
func (w *Workspace) NewTaskWorktree(path, id, title, source string, scope ...string) (*Workspace, *board.Task, error) {
	if strings.TrimSpace(title) == "" {
		return nil, nil, errors.New("empty title")
	}
	ids, err := w.taskIDs()
	if err != nil {
		return nil, nil, err
	}
	if id == "" {
		id = nextID(ids, w.Config.TaskPrefix())
	} else if ids[id] {
		return nil, nil, fmt.Errorf("task %s already exists", id)
	}
	base := w.Config.Base
	if base == "" {
		base = "HEAD"
	}
	ws, err := CreateWorktree(w.Repo, id, base, path)
	if err != nil {
		return nil, nil, err
	}
	t, err := ws.NewTask(id, title, source, scope...)
	if err != nil {
		_ = RemoveWorktree(w.Repo, path, true, true)
		return nil, nil, err
	}
	return ws, t, ws.SetActiveTask(t.ID)
}
