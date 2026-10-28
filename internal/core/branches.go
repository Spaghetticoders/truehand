package core

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/evidence"
	"github.com/spaghetticoders/truehand/internal/plan"
	"gopkg.in/yaml.v3"
)

// Tasks committed on other local branches are shown read-only: with a branch per task the
// human sits on the base branch, and a task delivered on its branch would otherwise vanish
// from the board exactly when it is the human's turn to push and merge it.

// otherBranches lists the local branches not checked out anywhere: a branch checked out
// here or in another worktree has its tasks read live from that checkout, and listing it
// again would show each of them twice. A worktree deleted by hand checks nothing out.
func (w *Workspace) otherBranches() []string {
	out, err := w.Repo.Git("for-each-ref", "--format=%(refname:short)%00%(worktreepath)", "refs/heads")
	if err != nil {
		return nil
	}
	var branches []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		b, wt, _ := strings.Cut(line, "\x00")
		if b == "" {
			continue
		}
		if _, err := os.Stat(wt); wt == "" || err != nil {
			branches = append(branches, b)
		}
	}
	return branches
}

func (w *Workspace) branchFile(branch, id, name string) (string, error) {
	return w.Repo.Git("show", branch+":.truehand/tasks/"+id+"/"+name)
}

func (w *Workspace) branchTask(branch, id string) (*board.Task, error) {
	if err := board.ValidID(id); err != nil {
		return nil, err
	}
	out, err := w.branchFile(branch, id, "task.yaml")
	if err != nil {
		return nil, fmt.Errorf("task %s not found on %s", id, branch)
	}
	var t board.Task
	if err := yaml.Unmarshal([]byte(out), &t); err != nil {
		return nil, fmt.Errorf("task %s on %s: %w", id, branch, err)
	}
	return &t, nil
}

// branchTasks returns the tasks found only on other branches, the most recently updated
// copy when a task is on several.
func (w *Workspace) branchTasks(known map[string]bool) []BoardTask {
	found := map[string]BoardTask{}
	var order []string
	for _, branch := range w.otherBranches() {
		out, err := w.Repo.Git("ls-tree", "--name-only", branch+":.truehand/tasks")
		if err != nil {
			continue
		}
		for _, id := range strings.Split(strings.TrimSpace(out), "\n") {
			if id == "" || known[id] {
				continue
			}
			t, err := w.branchTask(branch, id)
			if err != nil {
				continue
			}
			prev, seen := found[id]
			if seen && !t.Updated.After(prev.Updated) {
				continue
			}
			if !seen {
				order = append(order, id)
			}
			bt := BoardTask{Task: t, Branch: branch, Missing: []string{}}
			if next := t.State.Next(); next != "" {
				bt.Next = string(next)
			}
			found[id] = bt
		}
	}
	tasks := make([]BoardTask, 0, len(order))
	for _, id := range order {
		tasks = append(tasks, found[id])
	}
	return tasks
}

// BranchDetail reads a task as committed on another branch: description, comments, plan
// and history. Gates and evidence are evaluated only on a checkout of that branch.
func (w *Workspace) BranchDetail(branch, id string) (*TaskDetail, error) {
	if strings.HasPrefix(branch, "-") {
		return nil, fmt.Errorf("invalid branch %q", branch)
	}
	if _, err := w.Repo.Git("check-ref-format", "--branch", branch); err != nil {
		return nil, fmt.Errorf("invalid branch %q", branch)
	}
	t, err := w.branchTask(branch, id)
	if err != nil {
		return nil, err
	}
	desc, _ := w.branchFile(branch, id, "description.md")
	d := &TaskDetail{
		TaskView: &TaskView{Task: t, Branch: branch, Description: desc, Comments: []board.Event{},
			Evidence: []EvidenceView{}, Approvals: []board.Approval{}},
		Plan:   PlanView{Problems: []plan.Problem{}},
		Events: []board.Event{},
	}
	if events, err := w.branchFile(branch, id, "events.jsonl"); err == nil {
		for _, line := range strings.Split(events, "\n") {
			var e board.Event
			if json.Unmarshal([]byte(line), &e) == nil && e.Type != "" {
				d.Events = append(d.Events, e)
				if e.Type == "comment" {
					d.Comments = append(d.Comments, e)
				}
			}
		}
	}
	if md, err := w.branchFile(branch, id, "plan.md"); err == nil {
		probs := plan.Check(md)
		if probs == nil {
			probs = []plan.Problem{}
		}
		d.Plan = PlanView{Exists: true, Markdown: md, SHA256: evidence.HashBytes([]byte(md)), Problems: probs, Started: len(probs) < len(plan.Sections)}
	}
	return d, nil
}
