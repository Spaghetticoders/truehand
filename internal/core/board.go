package core

import (
	"os"
	"path/filepath"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/evidence"
	"github.com/spaghetticoders/truehand/internal/gate"
	"github.com/spaghetticoders/truehand/internal/plan"
)

// BoardTask is one card of the board: the task, where it stands and what blocks it.
type BoardTask struct {
	*board.Task
	Active bool `json:"active"`
	Stale  bool `json:"stale"`
	Merged bool `json:"merged"`
	// Branch is set for a task committed on another local branch than the checked-out one;
	// such a task is shown read-only.
	Branch  string   `json:"branch,omitempty"`
	Next    string   `json:"next,omitempty"`
	Missing []string `json:"missing"`
}

// BoardView is everything a UI needs to draw one repo's board in a single call.
type BoardView struct {
	Root  string      `json:"root"`
	Name  string      `json:"name"`
	Tasks []BoardTask `json:"tasks"`
}

func (w *Workspace) Board() (*BoardView, error) {
	tasks, err := w.Store.List()
	if err != nil {
		return nil, err
	}
	v := &BoardView{Root: w.Repo.Root, Name: filepath.Base(w.Repo.Root), Tasks: []BoardTask{}}
	active := w.ActiveTask()
	for _, t := range tasks {
		bt := BoardTask{Task: t, Active: t.ID == active, Stale: w.Stale(t), Merged: w.Merged(t), Missing: []string{}}
		if next := t.State.Next(); next != "" {
			bt.Next = string(next)
			if res, err := w.Check(t, next); err == nil {
				for _, it := range res.Items {
					if it.Status == gate.StatusFail {
						bt.Missing = append(bt.Missing, it.Check)
					}
				}
			}
		}
		v.Tasks = append(v.Tasks, bt)
	}
	known := map[string]bool{}
	for _, t := range tasks {
		known[t.ID] = true
	}
	v.Tasks = append(v.Tasks, w.branchTasks(known)...)
	return v, nil
}

// PlanView is the plan as a reviewer sees it, with the hash an approval must name.
type PlanView struct {
	Exists bool `json:"exists"`
	// Started is false while plan.md is still the untouched template.
	Started  bool           `json:"started"`
	Markdown string         `json:"markdown"`
	SHA256   string         `json:"sha256,omitempty"`
	Problems []plan.Problem `json:"problems"`
}

// TaskDetail is TaskView plus the plan and the event log.
type TaskDetail struct {
	*TaskView
	Plan   PlanView      `json:"plan"`
	Events []board.Event `json:"events"`
}

func (w *Workspace) Detail(t *board.Task) (*TaskDetail, error) {
	v, err := w.View(t)
	if err != nil {
		return nil, err
	}
	d := &TaskDetail{TaskView: v, Plan: PlanView{Problems: []plan.Problem{}}, Events: []board.Event{}}
	if b, err := os.ReadFile(w.Store.PlanPath(t.ID)); err == nil {
		probs := plan.Check(string(b))
		d.Plan = PlanView{Exists: true, Markdown: string(b), SHA256: evidence.HashBytes(b), Problems: probs, Started: len(probs) < len(plan.Sections)}
		if d.Plan.Problems == nil {
			d.Plan.Problems = []plan.Problem{}
		}
	}
	if ev, err := w.Store.Events(t.ID); err == nil && ev != nil {
		d.Events = ev
	}
	return d, nil
}
