// Package ci is the hard lock: it decides whether a branch may be merged.
package ci

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/config"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/evidence"
	"github.com/spaghetticoders/truehand/internal/gate"
)

type TaskReport struct {
	ID     string        `json:"id"`
	State  string        `json:"state"`
	Pass   bool          `json:"pass"`
	Gates  []gate.Result `json:"gates"`
	Covers []string      `json:"covers"`
}

type Report struct {
	Base      string       `json:"base"`
	GatesFrom string       `json:"gates_from"`
	Pass      bool         `json:"pass"`
	Changed   []string     `json:"changed"`
	Uncovered []string     `json:"uncovered"`
	Tasks     []TaskReport `json:"tasks"`
	Problems  []string     `json:"problems"`
}

// Verify checks every change between the merge-base with baseRef and HEAD: each changed file
// must belong to a task touched on this branch, and each such task must be delivered with
// every gate still passing on the checked-out content.
func Verify(ws *core.Workspace, baseRef string) (*Report, error) {
	if baseRef == "" {
		baseRef = ws.Config.Base
	}
	r := &Report{Base: baseRef, Changed: []string{}, Uncovered: []string{}, Tasks: []TaskReport{}, Problems: []string{}}
	mb, err := ws.Repo.Git("merge-base", "HEAD", baseRef)
	if err != nil {
		return nil, fmt.Errorf("cannot find merge-base with %s (in CI, fetch full history: fetch-depth: 0): %w", baseRef, err)
	}
	mb = strings.TrimSpace(mb)
	// Gates come from the base branch: a branch cannot relax the rules it is judged by.
	// Config changes take effect once merged, after human review of the PR.
	r.GatesFrom = "branch (no config on " + baseRef + " yet)"
	if b, err := ws.Repo.Git("show", mb+":.truehand/config.yaml"); err == nil {
		cfg, err := config.Parse([]byte(b))
		if err != nil {
			return nil, fmt.Errorf("config on %s: %w", baseRef, err)
		}
		if err := gate.Validate(cfg); err != nil {
			return nil, fmt.Errorf("config on %s: %w", baseRef, err)
		}
		cp := *ws
		cp.Config = cfg
		ws = &cp
		r.GatesFrom = baseRef
	}
	out, err := ws.Repo.Git("diff", "--name-only", "--no-renames", mb, "HEAD")
	if err != nil {
		return nil, err
	}
	touched := map[string]bool{}
	for _, p := range strings.Split(strings.TrimSpace(out), "\n") {
		if p == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(p, ".truehand/tasks/"); ok {
			if id, _, ok := strings.Cut(rest, "/"); ok {
				touched[id] = true
			}
			continue
		}
		if strings.HasPrefix(p, ".truehand/") {
			continue
		}
		r.Changed = append(r.Changed, p)
	}
	covered := map[string]bool{}
	ids := make([]string, 0, len(touched))
	for id := range touched {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		t, err := ws.Store.Load(id)
		if err != nil {
			r.Problems = append(r.Problems, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		tr := TaskReport{ID: id, State: string(t.State), Pass: true, Covers: []string{}}
		if t.State != board.Delivered {
			tr.Pass = false
			r.Problems = append(r.Problems, fmt.Sprintf("%s is %s, not delivered", id, t.State))
		}
		// Every gate is evaluated, whatever task.yaml says the state is.
		for _, st := range board.States[1:] {
			res, err := ws.Check(t, st)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", id, err)
			}
			tr.Gates = append(tr.Gates, res)
			if !res.Pass {
				tr.Pass = false
				r.Problems = append(r.Problems, fmt.Sprintf("%s: gate %s is closed", id, st))
			}
		}
		if tr.Pass {
			base, err := ws.BaseOf(t)
			if err != nil {
				return nil, err
			}
			fp, err := evidence.Compute(ws.Repo, base)
			if err != nil {
				return nil, err
			}
			for p := range fp.Files {
				covered[p] = true
				tr.Covers = append(tr.Covers, p)
			}
			sort.Strings(tr.Covers)
		}
		r.Tasks = append(r.Tasks, tr)
	}
	for _, p := range r.Changed {
		if !covered[p] {
			r.Uncovered = append(r.Uncovered, p)
		}
	}
	if len(r.Uncovered) > 0 {
		r.Problems = append(r.Problems, fmt.Sprintf("%d changed file(s) are not covered by a delivered task", len(r.Uncovered)))
	}
	r.Pass = len(r.Problems) == 0
	return r, nil
}
