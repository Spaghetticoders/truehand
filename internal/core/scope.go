package core

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/gate"
	"github.com/spaghetticoders/truehand/internal/glob"
)

// ScopePlan is what widening a task's scope would add: the files it changed outside its scope
// that no other task's scope claims (what the in_scope gate lists); Add proposes them as
// patterns matching exactly those paths (a file named "**" stays one file).
type ScopePlan struct {
	Outside []string
	Add     []string
}

// PlanScope computes the ScopePlan of a task, read-only.
func (w *Workspace) PlanScope(id string) (*ScopePlan, error) {
	t, err := w.Store.Load(id)
	if err != nil {
		return nil, err
	}
	base, err := w.BaseOf(t)
	if err != nil {
		return nil, err
	}
	fp, err := w.fingerprint(t, base)
	if err != nil {
		return nil, err
	}
	env, err := gate.NewEnvAt(w.Repo, w.Config, w.Store, t, base, fp)
	if err != nil {
		return nil, err
	}
	if tip, _, err := w.tipOf(t); err == nil && tip != "" {
		env.AtPoint, env.Tip = true, tip
	}
	out, err := gate.OutsideScope(env)
	if err != nil {
		return nil, err
	}
	add := make([]string, len(out))
	for i, f := range out {
		add[i] = glob.Escape(f)
	}
	return &ScopePlan{Outside: out, Add: add}, nil
}

// WidenScope adds to a task's scope, never removing any, entries of PlanScope's Add: files the
// task changed outside its scope, each as a literal path; a human decides the task owns them
// (the in_scope gate asks for it). Only a "human:" actor may; the event names it.
// Evidence recorded on the old scope goes stale where the new files enter: the gates say so.
func (w *Workspace) WidenScope(id string, globs []string, actor string, detail map[string]any) (*board.Task, error) {
	if !strings.HasPrefix(actor, "human:") {
		return nil, fmt.Errorf("only a human widens a task's scope, not %s", actor)
	}
	t, err := w.Store.Load(id)
	if err != nil {
		return nil, err
	}
	if len(t.Scope) == 0 {
		return nil, fmt.Errorf("task %s has no scope: it already owns every file it changes", t.ID)
	}
	var added []string
	for _, g := range globs {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		if strings.ContainsAny(g, "\r\n\x00") || strings.HasPrefix(g, "/") || strings.HasPrefix(g, "!") {
			return nil, fmt.Errorf("invalid glob %q: a path pattern relative to the repo, one line", g)
		}
		if !slices.Contains(t.Scope, g) && !slices.Contains(added, g) {
			added = append(added, g)
		}
	}
	if len(added) == 0 {
		return nil, errors.New("nothing to add: every glob is already in the scope")
	}
	// Only the files the task changed outside its scope, each as exactly itself: what the
	// person reads in the proposal is what the task gets.
	plan, err := w.PlanScope(id)
	if err != nil {
		return nil, err
	}
	for _, g := range added {
		if !slices.Contains(plan.Add, g) {
			return nil, fmt.Errorf("%q is not one of the files %s changed outside its scope: widen with the proposed paths", g, t.ID)
		}
	}
	t.Scope = append(t.Scope, added...)
	t.Updated = time.Now().UTC()
	if err := w.Store.Save(t); err != nil {
		return nil, err
	}
	w.forget()
	d := map[string]any{}
	for k, v := range detail {
		d[k] = v
	}
	d["added"], d["scope"] = added, t.Scope
	_ = w.Store.AppendEvent(t.ID, board.Event{Type: "scope-widened", Actor: actor, Detail: d})
	return t, nil
}
