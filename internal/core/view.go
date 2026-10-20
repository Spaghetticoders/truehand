package core

import (
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
	Task      *board.Task      `json:"task"`
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
	now, err := evidence.Compute(w.Repo, base)
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

// Stale reports whether a task past building has evidence that no longer holds: changed
// content, a later failing run or tampered files. Tasks are not moved back automatically
// (reads never write); the board flags them and CI refuses them.
func (w *Workspace) Stale(t *board.Task) bool {
	if t.State.Index() < board.Verified.Index() {
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
