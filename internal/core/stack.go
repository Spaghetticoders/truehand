package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/config"
	"github.com/spaghetticoders/truehand/internal/evidence"
	"github.com/spaghetticoders/truehand/internal/gate"
	"github.com/spaghetticoders/truehand/internal/glob"
)

// Stacked tasks: a task started on top of another task's commits, before those reach the base
// branch, measures its change from the point that closes the other task (its Base), not from
// the base branch. That is allowed only while every commit it skips is covered by tasks
// already reviewed or delivered: their files are theirs, judged at that point.

// maxStackSearch bounds how many commits restack looks back for the point.
const maxStackSearch = 200

func stackable(s board.State) bool { return s == board.Reviewed || s == board.Delivered }

// segment is the part of a chain one task owns: its change from base to tip.
type segment struct {
	task      *board.Task
	base, tip string
}

// chain returns the segments that lead up to tip through pred: pred owns base(pred)..tip, the
// task pred is stacked on owns its part up to pred's Base, and so on down to a task measured
// from the base branch.
func (w *Workspace) chain(pred *board.Task, tip string) ([]segment, error) {
	var segs []segment
	seen := map[string]bool{}
	for t := pred; ; {
		if seen[t.ID] || len(seen) > 32 {
			return nil, fmt.Errorf("stack through %s loops", t.ID)
		}
		seen[t.ID] = true
		if !stackable(t.State) {
			return nil, fmt.Errorf("%s is %s: a task stacks only on reviewed or delivered tasks", t.ID, t.State)
		}
		base, err := w.BaseOf(t)
		if err != nil {
			return nil, err
		}
		if base != tip && !isAncestor(w, base, tip) {
			return nil, fmt.Errorf("%s starts at %s, which %s does not contain", t.ID, short(base), short(tip))
		}
		segs = append(segs, segment{t, base, tip})
		if t.StackedOn == "" {
			return segs, nil
		}
		next, err := w.Store.Load(t.StackedOn)
		if err != nil {
			return nil, err
		}
		t, tip = next, t.Base
	}
}

// holds reports whether a task can stack on pred at point: every task of the chain is reviewed
// or delivered with its gates passing at its own tip, and together they cover every file the
// skipped commits change.
func (w *Workspace) holds(pred *board.Task, point string, ignore []string) error {
	segs, err := w.chain(pred, point)
	if err != nil {
		return err
	}
	if err := w.covers(segs); err != nil {
		return err
	}
	for i, s := range segs {
		ign := ignore
		if i > 0 {
			ign = w.signedIgnore(segs[i-1].task)
		}
		if err := w.gatesAt(s.task, s.tip, ign); err != nil {
			return fmt.Errorf("%s: %w", s.task.ID, err)
		}
	}
	return nil
}

// IgnoreHash names a list of ignored files: the sha256 of the sorted paths, one per line.
func IgnoreHash(files []string) string {
	if len(files) == 0 {
		return ""
	}
	sorted := slices.Clone(files)
	slices.Sort(sorted)
	h := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return hex.EncodeToString(h[:])
}

// signedIgnore is c's StackedIgnore when a human accepted exactly that list for exactly this
// stack (a "restacked" event by a human: actor, same base, task and hash); else none. A list an
// agent writes into task.yaml is not enough.
func (w *Workspace) signedIgnore(c *board.Task) []string {
	if len(c.StackedIgnore) == 0 {
		return nil
	}
	evs, err := w.Store.Events(c.ID)
	if err != nil {
		return nil
	}
	want := IgnoreHash(c.StackedIgnore)
	for i := len(evs) - 1; i >= 0; i-- {
		e := evs[i]
		if e.Type != "restacked" {
			continue
		}
		if strings.HasPrefix(e.Actor, "human:") && e.Detail["ignore_hash"] == want && e.Detail["base"] == c.Base && e.Detail["stacked_on"] == c.StackedOn {
			return c.StackedIgnore
		}
		return nil
	}
	return nil
}

// dirtAt lists the files a task's latest evidence saw that are not part of its change at
// point: what its working tree held besides its commits (another task's files, untracked
// leftovers, files out of a scope narrowed later, and files of its own never committed).
func (w *Workspace) dirtAt(x *board.Task, point string) ([]string, error) {
	base, err := w.BaseOf(x)
	if err != nil {
		return nil, err
	}
	fp, err := evidence.ComputeAt(w.Repo, base, point, x.Scope)
	if err != nil {
		return nil, err
	}
	recs, err := evidence.Log{Store: w.Store, Task: x.ID}.Records()
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, r := range evidence.LatestAll(recs) {
		for f := range r.Inputs.Files {
			if _, in := fp.Files[f]; !in {
				set[f] = true
			}
		}
	}
	dirt := make([]string, 0, len(set))
	for f := range set {
		dirt = append(dirt, f)
	}
	slices.Sort(dirt)
	return dirt, nil
}

// covers checks that every file the chain's commits change, from the bottom's base to the top's
// tip, belongs to the task that last changed it (its scope): none is left to nobody.
func (w *Workspace) covers(segs []segment) error {
	bottom, top := segs[len(segs)-1].base, segs[0].tip
	all, err := changedBetween(w, bottom, top)
	if err != nil {
		return err
	}
	diffs := make([]map[string]bool, len(segs))
	for i, s := range segs {
		files, err := changedBetween(w, s.base, s.tip)
		if err != nil {
			return err
		}
		diffs[i] = map[string]bool{}
		for _, f := range files {
			diffs[i][f] = true
		}
	}
	var loose []string
	for _, f := range all {
		owner := -1
		for i := range segs {
			if diffs[i][f] {
				owner = i
				break
			}
		}
		if owner < 0 || (len(segs[owner].task.Scope) > 0 && !glob.MatchAny(segs[owner].task.Scope, f)) {
			loose = append(loose, f)
		}
	}
	if len(loose) > 0 {
		if len(loose) > 5 {
			loose = append(loose[:5], "…")
		}
		return fmt.Errorf("commits up to %s change files no reviewed task covers: %s", short(top), strings.Join(loose, ", "))
	}
	return nil
}

func changedBetween(w *Workspace, a, b string) ([]string, error) {
	out, err := w.git("diff", "--name-only", "--no-renames", "-z", a, b)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(strings.TrimSuffix(out, "\x00"), "\x00") {
		if f != "" && f != ".truehand" && !strings.HasPrefix(f, ".truehand/") {
			files = append(files, f)
		}
	}
	return files, nil
}

// stackBase is the base of a stacked task: its Base, while the chain under it still holds.
// When it does not, the task has no base at all rather than one that hides commits.
func (w *Workspace) stackBase(t *board.Task) (string, error) {
	head, err := w.git("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if b := t.Base; b != strings.TrimSpace(head) && !isAncestor(w, b, strings.TrimSpace(head)) {
		return "", fmt.Errorf("task %s is stacked on %s at %s, which HEAD no longer contains: run `truehand task restack %s`", t.ID, t.StackedOn, short(t.Base), t.ID)
	}
	pred, err := w.Store.Load(t.StackedOn)
	if err != nil {
		return "", fmt.Errorf("task %s is stacked on %s: %w", t.ID, t.StackedOn, err)
	}
	if err := w.holds(pred, t.Base, w.signedIgnore(t)); err != nil {
		return "", fmt.Errorf("task %s is stacked on %s, but %v: run `truehand task restack %s`", t.ID, t.StackedOn, err, t.ID)
	}
	return t.Base, nil
}

// TipOf is the commit a task is judged at because another task, approved at least, is stacked
// on it with a stack that holds (the earliest such point HEAD contains), or "" when it is
// judged on the working tree.
func (w *Workspace) TipOf(t *board.Task) (string, error) {
	tip, _, err := w.tipOf(t)
	return tip, err
}

// tipOf is TipOf with the files the closing task's restack accepted to ignore.
func (w *Workspace) tipOf(t *board.Task) (string, []string, error) {
	tasks, err := w.listTasks()
	if err != nil {
		return "", nil, err
	}
	head, err := w.git("rev-parse", "HEAD")
	if err != nil {
		return "", nil, nil
	}
	head = strings.TrimSpace(head)
	tip := ""
	var ignore []string
	for _, c := range tasks {
		if c.StackedOn != t.ID || c.ID == t.ID {
			continue
		}
		if c.State == board.Planned || (c.Base != head && !isAncestor(w, c.Base, head)) {
			continue
		}
		// Only a stack that holds closes t: a task.yaml naming t is not enough.
		if b, err := w.BaseOf(c); err != nil || b != c.Base {
			continue
		}
		if tip == "" || isAncestor(w, c.Base, tip) {
			tip, ignore = c.Base, w.signedIgnore(c)
		}
	}
	return tip, ignore, nil
}

// Fingerprint is the task's content as its gates and CI see it: from its base to its tip
// when a task is stacked on it, else to the working tree.
func (w *Workspace) Fingerprint(t *board.Task) (evidence.Fingerprint, error) {
	base, err := w.BaseOf(t)
	if err != nil {
		return evidence.Fingerprint{}, err
	}
	return w.fingerprint(t, base)
}

var errNoStack = errors.New("no stack")

// stackFor finds the task a new task at point should stack on: a reviewed or delivered task
// whose chain covers every commit from the base branch to point and whose gates pass there.
// Of several, the one starting latest; errNoStack when none qualifies, with the reasons. With
// lenient, a task whose gates pass only once its working-tree dirt is ignored also
// qualifies, and that dirt is returned: a human must accept it (RestackWith).
func (w *Workspace) stackFor(point string, lenient bool, except ...string) (*board.Task, []string, []string, error) {
	tasks, err := w.Store.List()
	if err != nil {
		return nil, nil, nil, err
	}
	var best *board.Task
	var bestIgnore []string
	bestBase := ""
	var why []string
	for _, x := range tasks {
		if slices.Contains(except, x.ID) || !stackable(x.State) {
			continue
		}
		var ignore []string
		err := w.holds(x, point, nil)
		if err != nil && lenient {
			if dirt, derr := w.dirtAt(x, point); derr == nil && len(dirt) > 0 && w.holds(x, point, dirt) == nil {
				ignore, err = dirt, nil
			}
		}
		if err != nil {
			why = append(why, x.ID+": "+err.Error())
			continue
		}
		base, _ := w.BaseOf(x)
		if best == nil || isAncestor(w, bestBase, base) {
			best, bestIgnore, bestBase = x, ignore, base
		}
	}
	if best == nil {
		return nil, nil, why, errNoStack
	}
	return best, bestIgnore, nil, nil
}

// gatesAt requires every gate up to x's state to pass with x judged at tip.
func (w *Workspace) gatesAt(x *board.Task, tip string, ignore []string) error {
	base, err := w.BaseOf(x)
	if err != nil {
		return err
	}
	fp, err := evidence.ComputeAt(w.Repo, base, tip, x.Scope)
	if err != nil {
		return err
	}
	env, err := gate.NewEnvAt(w.Repo, w.Config, w.Store, x, base, fp)
	if err != nil {
		return err
	}
	env.ApproversRef = w.ApproversRef
	env.Approvers = func() ([]config.Approver, error) { return w.Approvers(x) }
	env.AtPoint = true
	env.Ignore = ignore
	for _, st := range board.States[1:] {
		if res := gate.Evaluate(env, st); !res.Pass {
			for _, it := range res.Items {
				if it.Status != "pass" {
					return fmt.Errorf("gate %s at %s: %s", st, short(tip), it.Message)
				}
			}
			return fmt.Errorf("gate %s is closed at %s", st, short(tip))
		}
		if st == x.State {
			break
		}
	}
	return nil
}

// stackOnCreate makes a new task stacked when HEAD is ahead of the base branch with commits
// that reviewed tasks cover: then its change starts at HEAD.
func (w *Workspace) stackOnCreate(t *board.Task) {
	if t.BaseRef == "" || !w.Repo.HasCommits() {
		return
	}
	head, err := w.Repo.Git("rev-parse", "HEAD")
	if err != nil {
		return
	}
	head = strings.TrimSpace(head)
	if t.Base == head {
		return
	}
	pred, _, _, err := w.stackFor(head, false, t.ID)
	if err != nil {
		return
	}
	t.Base, t.StackedOn = head, pred.ID
}

// RestackPlan is where a task's base would move and what a human must accept for it.
type RestackPlan struct {
	Point string
	Pred  *board.Task
	// Ignore lists the files Pred's evidence saw in its working tree but that are not in its
	// commits up to Point; IgnoreHash names the list. Empty when the stack holds as it is.
	Ignore     []string
	IgnoreHash string
}

// PlanRestack finds where a task's base would move: the latest commit on HEAD's first-parent
// history, after the base branch, that closes a reviewed or delivered task (every commit up to
// it covered, see stackFor). The commits after it stay the task's own. A point that needs
// files ignored comes with their list, for a human to read before accepting.
func (w *Workspace) PlanRestack(id string) (*RestackPlan, error) {
	t, err := w.Store.Load(id)
	if err != nil {
		return nil, err
	}
	if t.BaseRef == "" {
		return nil, fmt.Errorf("task %s has no base ref: nothing to stack on", t.ID)
	}
	mb, err := w.Repo.Git("merge-base", "HEAD", t.BaseRef)
	if err != nil {
		return nil, fmt.Errorf("no merge-base with %s: %w", t.BaseRef, err)
	}
	out, err := w.Repo.Git("rev-list", "--first-parent", fmt.Sprintf("--max-count=%d", maxStackSearch), strings.TrimSpace(mb)+"..HEAD")
	if err != nil {
		return nil, err
	}
	except := w.notUnder(t)
	var why []string
	for _, point := range strings.Fields(out) {
		pred, ignore, reasons, err := w.stackFor(point, true, except...)
		if err == nil {
			return &RestackPlan{Point: point, Pred: pred, Ignore: ignore, IgnoreHash: IgnoreHash(ignore)}, nil
		}
		if why == nil {
			why = reasons
		}
	}
	msg := fmt.Sprintf("no commit between %s and HEAD closes a reviewed or delivered task", t.BaseRef)
	if len(why) > 0 {
		msg += " (" + strings.Join(why, "; ") + ")"
	}
	return nil, errors.New(msg)
}

// RestackPoint is PlanRestack for a point that needs nothing ignored.
func (w *Workspace) RestackPoint(id string) (string, *board.Task, error) {
	p, err := w.PlanRestack(id)
	if err != nil {
		return "", nil, err
	}
	if len(p.Ignore) > 0 {
		return "", nil, fmt.Errorf("restacking %s on %s needs %d file(s) of its working tree ignored: use PlanRestack and show them", id, p.Pred.ID, len(p.Ignore))
	}
	return p.Point, p.Pred, nil
}

// notUnder lists t and the tasks stacked on it: none of them can be what t stacks on.
func (w *Workspace) notUnder(t *board.Task) []string {
	ids := []string{t.ID}
	all, _ := w.Store.List()
	for changed := true; changed; {
		changed = false
		for _, c := range all {
			if c.StackedOn != "" && slices.Contains(ids, c.StackedOn) && !slices.Contains(ids, c.ID) {
				ids = append(ids, c.ID)
				changed = true
			}
		}
	}
	return ids
}

// Restack moves a task's base to point, stacked on pred, checking again that the stack holds
// there. Only a human may ask for it; the caller checks that.
func (w *Workspace) Restack(id, point, pred string) (*board.Task, error) {
	return w.RestackBy(id, point, pred, w.Actor(), nil)
}

// RestackBy is Restack recorded as done by actor (the person who confirmed it in the app),
// with detail added to the event.
func (w *Workspace) RestackBy(id, point, pred, actor string, detail map[string]any) (*board.Task, error) {
	return w.RestackWith(id, point, pred, "", actor, detail)
}

// RestackWith is RestackBy accepting the files ignoreHash names (PlanRestack's IgnoreHash):
// the plan is computed again and must need exactly that list. Ignoring files takes a human
// actor ("human:…"); the list goes into task.yaml and the event, which is what makes it count.
func (w *Workspace) RestackWith(id, point, pred, ignoreHash, actor string, detail map[string]any) (*board.Task, error) {
	t, err := w.Store.Load(id)
	if err != nil {
		return nil, err
	}
	got, ignore, _, err := w.stackFor(point, true, w.notUnder(t)...)
	if err != nil || got.ID != pred {
		return nil, fmt.Errorf("task %s cannot stack on %s at %s any more: run restack again", t.ID, pred, short(point))
	}
	if h := IgnoreHash(ignore); h != ignoreHash {
		if len(ignore) == 0 {
			return nil, fmt.Errorf("task %s stacks on %s at %s with nothing to ignore: the accepted list no longer applies, run restack again", t.ID, pred, short(point))
		}
		return nil, fmt.Errorf("stacking %s on %s at %s ignores %d file(s) that were not the ones accepted: run restack again and read the list", t.ID, pred, short(point), len(ignore))
	}
	if len(ignore) > 0 && !strings.HasPrefix(actor, "human:") {
		return nil, fmt.Errorf("only a human can accept files to ignore, not %s", actor)
	}
	from := t.Base
	t.Base, t.StackedOn, t.StackedIgnore = point, pred, ignore
	t.Updated = time.Now().UTC()
	if err := w.Store.Save(t); err != nil {
		return nil, err
	}
	w.forget()
	d := map[string]any{}
	for k, v := range detail {
		d[k] = v
	}
	d["from"], d["base"], d["stacked_on"] = from, point, pred
	if len(ignore) > 0 {
		d["ignore"], d["ignore_hash"] = ignore, IgnoreHash(ignore)
	} else {
		delete(d, "ignore")
		delete(d, "ignore_hash")
	}
	_ = w.Store.AppendEvent(t.ID, board.Event{Type: "restacked", Actor: actor, Detail: d})
	return t, nil
}

func (w *Workspace) listTasks() ([]*board.Task, error) {
	if w.memo == nil {
		return w.Store.List()
	}
	w.memo.mu.Lock()
	tasks := w.memo.tasks
	w.memo.mu.Unlock()
	if tasks != nil {
		return tasks, nil
	}
	tasks, err := w.Store.List()
	if err != nil {
		return nil, err
	}
	if tasks == nil {
		tasks = []*board.Task{}
	}
	w.memo.mu.Lock()
	w.memo.tasks = tasks
	w.memo.mu.Unlock()
	return tasks, nil
}

// forget drops what a session remembered, after the board changed under it.
func (w *Workspace) forget() {
	if w.memo != nil {
		w.memo.mu.Lock()
		w.memo.base = map[string]string{}
		w.memo.fp = map[string]evidence.Fingerprint{}
		w.memo.tasks = nil
		w.memo.mu.Unlock()
	}
}

func short(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}
