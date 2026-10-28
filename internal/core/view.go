package core

import (
	"gopkg.in/yaml.v3"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/evidence"
	"github.com/spaghetticoders/truehand/internal/gate"
)

type EvidenceView struct {
	evidence.Record
	Fresh  bool     `json:"fresh"`
	Stale  []string `json:"stale,omitempty"`
	Intact bool     `json:"intact"`
}

type TaskView struct {
	Task        *board.Task `json:"task"`
	Description string      `json:"description"`
	// Branch is set when the task was read from another branch than the checked-out one.
	Branch string `json:"branch,omitempty"`
	// Comments are every comment on the task, newest last, including those added after
	// context.md was written.
	Comments  []board.Event    `json:"comments"`
	Active    bool             `json:"active"`
	NextGate  *gate.Result     `json:"next_gate,omitempty"`
	Evidence  []EvidenceView   `json:"evidence"`
	Approvals []board.Approval `json:"approvals"`
}

// Evidence returns a task's evidence with freshness and integrity, latest per hand/key
// unless all is set.
func (w *Workspace) Evidence(t *board.Task, all bool) ([]EvidenceView, error) {
	log := evidence.Log{Store: w.Store, Task: t.ID}
	recs, err := log.Records()
	if err != nil {
		return nil, err
	}
	if !all {
		recs = evidence.LatestAll(recs)
	}
	base, err := w.BaseOf(t)
	if err != nil {
		return nil, err
	}
	now, err := w.fingerprint(t, base)
	if err != nil {
		return nil, err
	}
	views := []EvidenceView{}
	for _, r := range recs {
		stale := r.Inputs.Stale(now)
		views = append(views, EvidenceView{Record: r, Fresh: len(stale) == 0, Stale: stale, Intact: log.Integrity(r) == nil})
	}
	return views, nil
}

// View is everything a UI or an agent needs to know about a task.
func (w *Workspace) View(t *board.Task) (*TaskView, error) {
	v := &TaskView{Task: t, Active: w.ActiveTask() == t.ID}
	if next := t.State.Next(); next != "" {
		res, err := w.Check(t, next)
		if err != nil {
			return nil, err
		}
		v.NextGate = &res
	}
	var err error
	if v.Description, err = w.Store.Description(t.ID); err != nil {
		return nil, err
	}
	v.Comments = []board.Event{}
	if events, err := w.Store.Events(t.ID); err == nil {
		for _, e := range events {
			if e.Type == "comment" {
				v.Comments = append(v.Comments, e)
			}
		}
	}
	if v.Evidence, err = w.Evidence(t, false); err != nil {
		return nil, err
	}
	if v.Approvals, err = w.Store.Approvals(t.ID); err != nil {
		return nil, err
	}
	if v.Approvals == nil {
		v.Approvals = []board.Approval{}
	}
	return v, nil
}

// Merged reports whether the task was delivered on the base branch: its record there says
// delivered. Later commits on the base change what its evidence ran on, but the work was
// checked when it was delivered (and by CI when it was pushed), so it is done, not stale.
func (w *Workspace) Merged(t *board.Task) bool {
	if t.State != board.Delivered || t.BaseRef == "" {
		return false
	}
	out, err := w.Repo.Git("show", t.BaseRef+":.truehand/tasks/"+t.ID+"/task.yaml")
	if err != nil {
		return false
	}
	var onBase board.Task
	return yaml.Unmarshal([]byte(out), &onBase) == nil && onBase.State == board.Delivered
}

// Stale reports whether a task past building has evidence that no longer holds: changed
// content, a later failing run or tampered files. Tasks are not moved back automatically
// (reads never write); the board flags them and CI refuses them.
func (w *Workspace) Stale(t *board.Task) bool {
	if t.State.Index() < board.Verified.Index() || w.Merged(t) {
		return false
	}
	views, err := w.Evidence(t, false)
	if err != nil {
		return true
	}
	for _, v := range views {
		if !v.Fresh || !v.Intact || v.Verdict != evidence.Pass {
			return true
		}
	}
	return false
}
