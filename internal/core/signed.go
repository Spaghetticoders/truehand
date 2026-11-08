package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/config"
	"github.com/spaghetticoders/truehand/internal/evidence"
	"github.com/spaghetticoders/truehand/internal/gate"
	"github.com/spaghetticoders/truehand/internal/gitx"
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

// VerifyCommitConfig checks a signed intent to commit the config whose sha256 is configSHA
// onto the base commit base, by an approver the config committed at base lists, then spends
// its nonce. Which commit is the base's tip, and that the config parses, are the caller's to
// check: a config that does not load locks every later approval out.
func (w *Workspace) VerifyCommitConfig(base, configSHA string, s sign.Signed) (*config.Approver, error) {
	return w.verifyConfigIntent(sign.ActionCommitConfig, base, base, configSHA, s)
}

// VerifyPushBase checks a signed intent to push head to the base whose remote tip is from: the
// approvers are those the config committed at from lists, head must descend from it and change
// nothing but .truehand/config.yaml, which must load, and the intent's plan_sha256 is the
// sha256 of that file at head. It spends the nonce.
func (w *Workspace) VerifyPushBase(from, head string, s sign.Signed) (*config.Approver, error) {
	b, err := w.CheckPushBase(from, head)
	if err != nil {
		return nil, err
	}
	return w.verifyConfigIntent(sign.ActionPushBase, from, head, evidence.HashBytes(b), s)
}

// PushBaseError is why a push of the base is refused, with a code for the app.
type PushBaseError struct {
	Code string // not-only-config | config-invalid | base-moved
	Err  error
}

func (e *PushBaseError) Error() string { return e.Err.Error() }
func (e *PushBaseError) Unwrap() error { return e.Err }

func pushBaseErr(code string, err error) error { return &PushBaseError{Code: code, Err: err} }

// CheckPushBase is what a push of the base from origin's tip from to head must be, however
// it is confirmed (an approver's signature or a person): head descends from from, every
// commit between is a plain commit changing only the config, the config at head loads, and
// it does not drop every approver. It returns the config's bytes at head.
func (w *Workspace) CheckPushBase(from, head string) ([]byte, error) {
	if !commitID(from) || !commitID(head) {
		return nil, errors.New("push-base: from and head must be commit ids")
	}
	if why := w.Repo.HistoryRewritten(); why != "" {
		return nil, fmt.Errorf("push-base: %s", why)
	}
	if _, err := w.Repo.Git(noReplace, "merge-base", "--is-ancestor", from, head); err != nil {
		return nil, pushBaseErr("base-moved", fmt.Errorf("push-base: %s does not descend from %s", head, from))
	}
	cfgPath := config.Dir + "/" + config.File
	if err := onlyConfigCommits(w.Repo, from, head, cfgPath); err != nil {
		return nil, pushBaseErr("not-only-config", err)
	}
	out, err := w.Repo.Git(noReplace, "cat-file", "blob", head+":"+cfgPath)
	if err != nil {
		return nil, pushBaseErr("config-invalid", fmt.Errorf("push-base: no %s at %s", cfgPath, head))
	}
	b := []byte(out)
	next, err := config.Parse(b)
	if err != nil {
		return nil, pushBaseErr("config-invalid", fmt.Errorf("push-base: the config at %s does not load: %w", head[:12], err))
	}
	before, err := gate.CommittedApprovers(w.Repo, from)
	if err != nil {
		return nil, err
	}
	if len(before) > 0 && len(next.Approvers) == 0 {
		return nil, pushBaseErr("config-invalid", errors.New("push-base: the config removes every approver: signed approvals would stop"))
	}
	return b, nil
}

func (w *Workspace) verifyConfigIntent(action, approversAt, head, configSHA string, s sign.Signed) (*config.Approver, error) {
	if why := w.Repo.HistoryRewritten(); why != "" {
		return nil, fmt.Errorf("%s: %s", action, why)
	}
	approvers, err := gate.CommittedApprovers(w.Repo, approversAt)
	if err != nil {
		return nil, err
	}
	if len(approvers) == 0 {
		return nil, ErrNoApprovers
	}
	if err := s.Intent.Fresh(timeNow(), SignedIntentMaxAge); err != nil {
		return nil, err
	}
	ap, err := gate.VerifySigned(gate.RepoID(w.Repo), approvers, action, sign.ConfigTask, configSHA, head, &s)
	if err != nil {
		return nil, err
	}
	if err := w.spendNonce(s.Intent.Nonce); err != nil {
		return nil, err
	}
	return ap, nil
}

// onlyConfigCommits requires every commit in from..head to be a non-merge commit changing
// nothing but path: a signature to push a config carries no code with it.
func onlyConfigCommits(repo *gitx.Repo, from, head, path string) error {
	out, err := repo.Git(noReplace, "rev-list", "--parents", from+".."+head)
	if err != nil {
		return err
	}
	lines := strings.Fields(strings.ReplaceAll(out, " ", "\x00"))
	if len(lines) == 0 {
		return errors.New("push-base: nothing to push")
	}
	for _, l := range lines {
		ids := strings.Split(l, "\x00")
		if len(ids) != 2 {
			return fmt.Errorf("push-base: %s is a merge or root commit", ids[0][:12])
		}
		changed, err := repo.Git(noReplace, "diff-tree", "--no-commit-id", "--no-renames", "--name-only", "-r", "-z", ids[1], ids[0])
		if err != nil {
			return err
		}
		if changed != "" && changed != path+"\x00" {
			return fmt.Errorf("push-base: commit %s changes more than %s: %s", ids[0][:12], path, strings.ReplaceAll(strings.TrimSuffix(changed, "\x00"), "\x00", ", "))
		}
	}
	return nil
}

// noReplace: see gate; what is pushed is the real object, not its local replacement.
const noReplace = "--no-replace-objects"

var commitIDRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

func commitID(s string) bool { return commitIDRe.MatchString(s) }

// spendNonce records a push, open-pr or config nonce in the git dir and refuses one already
// spent, under a lock so that concurrent requests carrying one intent spend it once.
func (w *Workspace) spendNonce(nonce string) error {
	gd, err := w.Repo.GitDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(gd, "truehand")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	unlock, err := lockFile(filepath.Join(dir, "intents.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	p := filepath.Join(dir, "intents.jsonl")
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
