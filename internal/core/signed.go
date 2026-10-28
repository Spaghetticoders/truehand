package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/config"
	"github.com/spaghetticoders/truehand/internal/evidence"
	"github.com/spaghetticoders/truehand/internal/gate"
	"github.com/spaghetticoders/truehand/internal/plan"
	"github.com/spaghetticoders/truehand/internal/sign"
)

// SignedIntentMaxAge is how far a signed intent's time may be from now when it arrives.
const SignedIntentMaxAge = 5 * time.Minute

// ErrNoApprovers: the base branch lists no approvers, so signed intents cannot be checked.
var ErrNoApprovers = errors.New("no approvers on the base branch: add approvers to .truehand/config.yaml there")

var timeNow = time.Now

// Approvers returns the approvers that count for a task: those on its base branch.
func (w *Workspace) Approvers(t *board.Task) ([]config.Approver, error) {
	if w.memo == nil {
		return gate.BaseApprovers(w.Repo, w.Config, t, w.ApproversRef)
	}
	key := t.BaseRef + "\x00" + t.Base
	w.memo.mu.Lock()
	list, ok := w.memo.appr[key]
	w.memo.mu.Unlock()
	if ok {
		return slices.Clone(list), nil
	}
	list, err := gate.BaseApprovers(w.Repo, w.Config, t, w.ApproversRef)
	if err != nil {
		return nil, err
	}
	w.memo.mu.Lock()
	w.memo.appr[key] = slices.Clone(list)
	w.memo.mu.Unlock()
	return list, nil
}

// ApproveSigned records a plan approval signed by an approver and moves a planned task to
// approved. The intent must name this repo, task and plan, be fresh, and use a new nonce.
func (w *Workspace) ApproveSigned(id string, s sign.Signed) (*board.Task, board.Approval, error) {
	t, err := w.Store.Load(id)
	if err != nil {
		return nil, board.Approval{}, err
	}
	b, err := os.ReadFile(w.Store.PlanPath(t.ID))
	if err != nil {
		return nil, board.Approval{}, fmt.Errorf("no plan.md to approve for %s", t.ID)
	}
	if probs := plan.Check(string(b)); len(probs) > 0 {
		return nil, board.Approval{}, fmt.Errorf("plan.md is incomplete (%d problem(s))", len(probs))
	}
	sha := evidence.HashBytes(b)
	approvers, err := w.Approvers(t)
	if err != nil {
		return nil, board.Approval{}, err
	}
	if len(approvers) == 0 {
		return nil, board.Approval{}, ErrNoApprovers
	}
	if err := s.Intent.Fresh(timeNow(), SignedIntentMaxAge); err != nil {
		return nil, board.Approval{}, err
	}
	ap, err := gate.VerifySignedApproval(gate.RepoID(w.Repo), approvers, t.ID, sha, &s)
	if err != nil {
		return nil, board.Approval{}, err
	}
	apps, err := w.Store.Approvals(t.ID)
	if err != nil {
		return nil, board.Approval{}, err
	}
	for _, a := range apps {
		if a.Sig != nil && a.Sig.Intent.Nonce == s.Intent.Nonce {
			return nil, board.Approval{}, errors.New("this signed approval was already used")
		}
	}
	a := board.Approval{
		State: board.Approved, Name: ap.Name, PlanSHA: sha,
		Via: "signed:" + s.Format + ":" + ap.ID, Nonce: s.Intent.Nonce, Sig: &s,
	}
	if err := w.Store.AppendApproval(t.ID, a); err != nil {
		return nil, board.Approval{}, err
	}
	_ = w.Store.AppendEvent(t.ID, board.Event{Type: "approved", Actor: "approver:" + ap.ID})
	if t.State == board.Planned {
		if _, err := w.Move(t, board.Approved); err != nil {
			return t, a, fmt.Errorf("approved, but the task did not move: %w", err)
		}
	}
	return t, a, nil
}

// VerifyIntent checks a signed push or open-pr intent for the task's current plan and head
// commit, then spends its nonce: the same signature never authorises twice in this clone.
func (w *Workspace) VerifyIntent(t *board.Task, action, head string, s sign.Signed) (*config.Approver, error) {
	if action != sign.ActionPush && action != sign.ActionOpenPR {
		return nil, fmt.Errorf("%q is not a push or open-pr intent", action)
	}
	b, err := os.ReadFile(w.Store.PlanPath(t.ID))
	if err != nil {
		return nil, fmt.Errorf("no plan.md for %s", t.ID)
	}
	approvers, err := w.Approvers(t)
	if err != nil {
		return nil, err
	}
	if len(approvers) == 0 {
		return nil, ErrNoApprovers
	}
	if err := s.Intent.Fresh(timeNow(), SignedIntentMaxAge); err != nil {
		return nil, err
	}
	ap, err := gate.VerifySigned(gate.RepoID(w.Repo), approvers, action, t.ID, evidence.HashBytes(b), head, &s)
	if err != nil {
		return nil, err
	}
	if err := w.spendNonce(s.Intent.Nonce); err != nil {
		return nil, err
	}
	return ap, nil
}

// spendNonce records a push/open-pr nonce in the git dir and refuses one already spent.
func (w *Workspace) spendNonce(nonce string) error {
	gd, err := w.Repo.GitDir()
	if err != nil {
		return err
	}
	p := filepath.Join(gd, "truehand", "intents.jsonl")
	spent := false
	if err := board.ReadJSONL(p, func(line []byte) error {
		var r struct{ Nonce string }
		if json.Unmarshal(line, &r) == nil && r.Nonce == nonce {
			spent = true
		}
		return nil
	}); err != nil {
		return err
	}
	if spent {
		return errors.New("this signed intent was already used")
	}
	return board.AppendJSONL(p, map[string]any{"nonce": nonce, "time": timeNow().UTC()})
}
