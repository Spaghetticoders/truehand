// Package core ties repo, config and board together; the CLI, MCP server and HTTP server
// all go through it so they behave identically.
package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/config"
	"github.com/spaghetticoders/truehand/internal/evidence"
	"github.com/spaghetticoders/truehand/internal/gate"
	"github.com/spaghetticoders/truehand/internal/gitx"
	"github.com/spaghetticoders/truehand/internal/tickets"
)

type Workspace struct {
	Repo   *gitx.Repo
	Config *config.Config
	Store  *board.Store
	// ApproversRef, when set, is the commit whose config lists the approvers (see gate.Env).
	ApproversRef string
	memo         *memo
}

// memo keeps what one read of the board or of a task computes several times per task.
type memo struct {
	mu   sync.Mutex
	base map[string]string
	fp   map[string]evidence.Fingerprint
	git  map[string]gitResult
	appr map[string][]config.Approver
}

type gitResult struct {
	out string
	err error
}

// git runs a read-only git command, once per session for the same arguments.
func (w *Workspace) git(args ...string) (string, error) {
	if w.memo == nil {
		return w.Repo.Git(args...)
	}
	key := strings.Join(args, "\x00")
	w.memo.mu.Lock()
	r, ok := w.memo.git[key]
	w.memo.mu.Unlock()
	if ok {
		return r.out, r.err
	}
	out, err := w.Repo.Git(args...)
	w.memo.mu.Lock()
	w.memo.git[key] = gitResult{out, err}
	w.memo.mu.Unlock()
	return out, err
}

// session returns a copy of w that remembers each task's base and fingerprint. It is for a
// single read: commits or edits made meanwhile are not seen.
func (w *Workspace) session() *Workspace {
	if w.memo != nil {
		return w
	}
	cp := *w
	cp.memo = &memo{base: map[string]string{}, fp: map[string]evidence.Fingerprint{}, git: map[string]gitResult{}, appr: map[string][]config.Approver{}}
	return &cp
}

// fingerprint is evidence.Compute for the task's scope on base, remembered in a session.
func (w *Workspace) fingerprint(t *board.Task, base string) (evidence.Fingerprint, error) {
	if w.memo == nil {
		return evidence.Compute(w.Repo, base, t.Scope)
	}
	key := base + "\x00" + strings.Join(t.Scope, "\x00")
	w.memo.mu.Lock()
	fp, ok := w.memo.fp[key]
	w.memo.mu.Unlock()
	if ok {
		return fp, nil
	}
	fp, err := evidence.Compute(w.Repo, base, t.Scope)
	if err != nil {
		return fp, err
	}
	w.memo.mu.Lock()
	w.memo.fp[key] = fp
	w.memo.mu.Unlock()
	return fp, nil
}

// Open finds the repo containing dir and loads its config.
func Open(dir string) (*Workspace, error) {
	repo, err := gitx.Open(dir)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(repo.Root)
	if err != nil {
		return nil, err
	}
	if err := gate.Validate(cfg); err != nil {
		return nil, err
	}
	return &Workspace{Repo: repo, Config: cfg, Store: &board.Store{Root: repo.Root}}, nil
}

// Actor identifies who runs a command. Inside a Claude Code session it is that session, and
// TRUEHAND_ACTOR cannot override it; elsewhere TRUEHAND_ACTOR (for other agent runners),
// else the human's git identity.
func (w *Workspace) Actor() string {
	if a := w.agentActor(); a != "" {
		return a
	}
	if a := strings.TrimSpace(os.Getenv("TRUEHAND_ACTOR")); a != "" {
		return a
	}
	if email := w.Repo.ConfigValue("user.email"); email != "" {
		return "human:" + email
	}
	return "human:unknown"
}

func (w *Workspace) activePath() (string, error) {
	gd, err := w.Repo.GitDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(gd, "truehand", "active"), nil
}

// ActiveTask returns the task id selected with `truehand task use`, or "".
func (w *Workspace) ActiveTask() string {
	p, err := w.activePath()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (w *Workspace) SetActiveTask(id string) error {
	p, err := w.activePath()
	if err != nil {
		return err
	}
	if id == "" {
		err := os.Remove(p)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(id+"\n"), 0o644)
}

// ResolveTask returns the given id, or the active task when id is empty.
func (w *Workspace) ResolveTask(id string) (*board.Task, error) {
	if id == "" {
		id = w.ActiveTask()
	}
	if id == "" {
		return nil, errors.New("no task given and no active task: pass a task id or run `truehand task use <id>`")
	}
	return w.Store.Load(id)
}

func (w *Workspace) NewTask(id, title, source string, scope ...string) (*board.Task, error) {
	if source != "" {
		if adapter, _ := tickets.ParseRef(source, w.Config.Tickets.Adapter); adapter == "" {
			return nil, fmt.Errorf("ticket %q has no tracker: write it as linear:%s, github:owner/repo#12 or file:brief.md, or set tickets.adapter in .truehand/config.yaml", source, source)
		}
	}
	if ref := w.Config.Base; ref != "" && w.Repo.HasCommits() {
		if _, err := w.Repo.Git("rev-parse", "--verify", "-q", ref+"^{commit}"); err != nil {
			return nil, fmt.Errorf("base ref %q does not exist in this repo: set `base:` in .truehand/config.yaml to the branch tasks are measured against (e.g. main)", ref)
		}
	}
	now := time.Now().UTC()
	t := &board.Task{
		ID: id, Title: title, Source: source, State: board.Planned,
		Base: w.Repo.Base(w.Config.Base), BaseRef: w.Config.Base, Scope: scope, Created: now, Updated: now,
	}
	if err := w.Store.Create(t); err != nil {
		return nil, err
	}
	if err := w.Store.AppendEvent(id, board.Event{Type: "created", Actor: w.Actor(), To: board.Planned, Detail: map[string]any{"base": t.Base}}); err != nil {
		return nil, err
	}
	return t, nil
}

// Comment appends a note to the task's log; the board shows it as the task's discussion.
func (w *Workspace) Comment(id, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("empty comment")
	}
	if _, err := w.Store.Load(id); err != nil {
		return err
	}
	return w.Store.AppendEvent(id, board.Event{Type: "comment", Actor: w.Actor(), Detail: map[string]any{"text": text}})
}

// BaseOf returns the commit a task's diff is measured against: the merge-base of HEAD with
// the task's base ref, so a rebase onto a newer base never pulls other people's commits into
// the task. On the base branch itself (trunk-based work) it is the commit the task started on.
func (w *Workspace) BaseOf(t *board.Task) (string, error) {
	if w.memo == nil {
		return w.baseOf(t)
	}
	key := t.BaseRef + "\x00" + t.Base
	w.memo.mu.Lock()
	b, ok := w.memo.base[key]
	w.memo.mu.Unlock()
	if ok {
		return b, nil
	}
	b, err := w.baseOf(t)
	if err != nil {
		return b, err
	}
	w.memo.mu.Lock()
	w.memo.base[key] = b
	w.memo.mu.Unlock()
	return b, nil
}

func (w *Workspace) baseOf(t *board.Task) (string, error) {
	if t.BaseRef == "" || t.Base == gitx.EmptyTree {
		return t.Base, nil
	}
	mb, err := w.git("merge-base", "HEAD", t.BaseRef)
	if err != nil {
		if _, e := w.Repo.Git("cat-file", "-e", t.Base+"^{commit}"); e == nil {
			return t.Base, nil
		}
		return "", fmt.Errorf("task %s: base ref %s not found and start commit %s missing (in CI, fetch full history)", t.ID, t.BaseRef, t.Base)
	}
	mb = strings.TrimSpace(mb)
	head, _ := w.git("rev-parse", "HEAD")
	if mb == strings.TrimSpace(head) {
		return t.Base, nil
	}
	if isAncestor(w, t.Base, mb) {
		return mb, nil
	}
	return t.Base, nil
}

// isAncestor reports whether a is an ancestor of b.
func isAncestor(w *Workspace, a, b string) bool {
	_, err := w.Repo.Git("merge-base", "--is-ancestor", a, b)
	return err == nil
}

// Check evaluates the gate for a target state without moving.
func (w *Workspace) Check(t *board.Task, target board.State) (gate.Result, error) {
	base, err := w.BaseOf(t)
	if err != nil {
		return gate.Result{}, err
	}
	fp, err := w.fingerprint(t, base)
	if err != nil {
		return gate.Result{}, err
	}
	env, err := gate.NewEnvAt(w.Repo, w.Config, w.Store, t, base, fp)
	if err != nil {
		return gate.Result{}, err
	}
	env.ApproversRef = w.ApproversRef
	env.Approvers = func() ([]config.Approver, error) { return w.Approvers(t) }
	return gate.Evaluate(env, target), nil
}

type MoveError struct {
	Result gate.Result
}

func (e *MoveError) Error() string {
	return fmt.Sprintf("gate %s is closed for %s", e.Result.Target, e.Result.Task)
}

// Move transitions a task. Forward moves must pass the target's gate; the result is
// returned in both cases so callers can show what passed.
func (w *Workspace) Move(t *board.Task, target board.State) (gate.Result, error) {
	forward, err := board.CheckMove(t.State, target)
	if err != nil {
		return gate.Result{}, err
	}
	res := gate.Result{Task: t.ID, Target: string(target), Pass: true}
	if forward {
		res, err = w.Check(t, target)
		if err != nil {
			return res, err
		}
		if !res.Pass {
			_ = w.Store.AppendEvent(t.ID, board.Event{Type: "gate_closed", Actor: w.Actor(), From: t.State, To: target, Detail: map[string]any{"items": res.Items}})
			return res, &MoveError{Result: res}
		}
	}
	from := t.State
	actor := w.Actor()
	if target == board.Building && (t.Roles == nil || t.Roles["developer"] == "") {
		if t.Roles == nil {
			t.Roles = map[string]string{}
		}
		t.Roles["developer"] = actor
	}
	t.State = target
	t.Updated = time.Now().UTC()
	if err := w.Store.Save(t); err != nil {
		return res, err
	}
	typ := "moved"
	if !forward {
		typ = "reworked"
	}
	return res, w.Store.AppendEvent(t.ID, board.Event{Type: typ, Actor: actor, From: from, To: target})
}
