// Package gate evaluates the rules that guard entering a state.
package gate

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/config"
	"github.com/spaghetticoders/truehand/internal/evidence"
	"github.com/spaghetticoders/truehand/internal/gitx"
	"github.com/spaghetticoders/truehand/internal/glob"
	"github.com/spaghetticoders/truehand/internal/plan"
)

const (
	StatusPass = "pass"
	StatusFail = "fail"
	StatusSkip = "skip"
)

type Item struct {
	Check   string `json:"check"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type Result struct {
	Task   string `json:"task"`
	Target string `json:"target"`
	Pass   bool   `json:"pass"`
	Items  []Item `json:"items"`
}

// Env is everything a check may look at. Fingerprint and Changed are computed once.
type Env struct {
	Repo     *gitx.Repo
	Config   *config.Config
	Store    *board.Store
	Task     *board.Task
	Evidence []evidence.Record
	Now      evidence.Fingerprint
	Changed  []string
	Base     string
	Events   []board.Event
}

func NewEnv(repo *gitx.Repo, cfg *config.Config, store *board.Store, t *board.Task, base string) (*Env, error) {
	recs, err := evidence.Log{Store: store, Task: t.ID}.Records()
	if err != nil {
		return nil, err
	}
	fp, err := evidence.Compute(repo, base, t.Scope)
	if err != nil {
		return nil, err
	}
	changed := make([]string, 0, len(fp.Files))
	for p := range fp.Files {
		changed = append(changed, p)
	}
	sort.Strings(changed)
	events, err := store.Events(t.ID)
	if err != nil {
		return nil, err
	}
	return &Env{Repo: repo, Config: cfg, Store: store, Task: t, Evidence: recs, Now: fp, Changed: changed, Base: base, Events: events}, nil
}

type checkFunc func(e *Env, r config.Rule) Item

var checks = map[string]checkFunc{
	"plan_valid":            checkPlanValid,
	"human_approval":        checkHumanApproval,
	"test_proof":            func(e *Env, r config.Rule) Item { return checkHand(e, r, "test-proof") },
	"eyes":                  func(e *Env, r config.Rule) Item { return checkHand(e, r, "eyes") },
	"hand_passed":           func(e *Env, r config.Rule) Item { return checkHand(e, r, str(r.With, "hand")) },
	"review_by_other_role":  checkReview,
	"evidence_fresh":        checkAllFresh,
	"evidence_matches_head": checkMatchesHead,
	"conventional_commits":  checkConventionalCommits,
}

// Known returns the names of all checks, sorted.
func Known() []string {
	var out []string
	for k := range checks {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Validate reports unknown checks in a config.
func Validate(cfg *config.Config) error {
	var errs []error
	for state, rules := range cfg.Gates {
		if _, err := board.ParseState(state); err != nil {
			errs = append(errs, fmt.Errorf("gates.%s: %w", state, err))
		}
		for i, r := range rules {
			if _, ok := checks[r.Check]; !ok {
				errs = append(errs, fmt.Errorf("gates.%s[%d]: unknown check %q (known: %s)", state, i, r.Check, strings.Join(Known(), ", ")))
			}
			if r.Check == "hand_passed" {
				h := str(r.With, "hand")
				switch {
				case h == "":
					errs = append(errs, fmt.Errorf("gates.%s[%d]: hand_passed needs with.hand", state, i))
				case cfg.Command(h) == "":
					// Without a configured command any command could stand in for the hand.
					errs = append(errs, fmt.Errorf("gates.%s[%d]: hand_passed %q needs commands.%s", state, i, h, h))
				}
			}
		}
	}
	return errors.Join(errs...)
}

// Evaluate runs every rule of the target state's gate.
func Evaluate(e *Env, target board.State) Result {
	res := Result{Task: e.Task.ID, Target: string(target), Pass: true, Items: []Item{}}
	for _, r := range e.Config.Gates[string(target)] {
		var it Item
		if r.When != nil && len(r.When.Changed) > 0 && !e.touches(r.When.Changed) {
			it = Item{Check: r.Check, Status: StatusSkip, Message: "no changed file matches " + strings.Join(r.When.Changed, ", ")}
		} else if fn, ok := checks[r.Check]; ok {
			it = fn(e, r)
			it.Check = r.Check
		} else {
			it = Item{Check: r.Check, Status: StatusFail, Message: "unknown check"}
		}
		if it.Status == StatusFail {
			res.Pass = false
		}
		res.Items = append(res.Items, it)
	}
	return res
}

func (e *Env) touches(patterns []string) bool {
	for _, p := range e.Changed {
		if glob.MatchAny(patterns, p) {
			return true
		}
	}
	return false
}

func pass(msg string, a ...any) Item {
	return Item{Status: StatusPass, Message: fmt.Sprintf(msg, a...)}
}
func fail(msg string, a ...any) Item {
	return Item{Status: StatusFail, Message: fmt.Sprintf(msg, a...)}
}

func str(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

func checkPlanValid(e *Env, _ config.Rule) Item {
	b, err := os.ReadFile(e.Store.PlanPath(e.Task.ID))
	if err != nil {
		return fail("plan.md not found: run `truehand plan init %s`", e.Task.ID)
	}
	if probs := plan.Check(string(b)); len(probs) > 0 {
		var msgs []string
		for _, p := range probs {
			msgs = append(msgs, p.Section+": "+p.Message)
		}
		return fail("plan.md is incomplete: %s", strings.Join(msgs, "; "))
	}
	return pass("plan.md has all required sections")
}

func checkHumanApproval(e *Env, _ config.Rule) Item {
	b, err := os.ReadFile(e.Store.PlanPath(e.Task.ID))
	if err != nil {
		return fail("no plan.md to approve")
	}
	sha := evidence.HashBytes(b)
	apps, err := e.Store.Approvals(e.Task.ID)
	if err != nil {
		return fail("approvals: %v", err)
	}
	stale := false
	for i := len(apps) - 1; i >= 0; i-- {
		a := apps[i]
		if a.State != board.Approved {
			continue
		}
		if a.PlanSHA == sha {
			return pass("approved by %s <%s> via %s", a.Name, a.Email, a.Via)
		}
		stale = true
	}
	if stale {
		return fail("plan.md changed after the last approval: a human must run `truehand approve %s` again", e.Task.ID)
	}
	return fail("waiting for a human: Approve plan on the task in the Truehand app, or `truehand approve %s` in a terminal", e.Task.ID)
}

// checkHand requires the latest record of a hand (per key) to pass, be intact and be fresh.
func checkHand(e *Env, r config.Rule, hand string) Item {
	if hand == "" {
		return fail("no hand configured")
	}
	latest := evidence.Latest(e.Evidence, hand)
	if len(latest) == 0 {
		return fail("no %s evidence: run `%s`", hand, HandCommand(hand))
	}
	log := evidence.Log{Store: e.Store, Task: e.Task.ID}
	keys := make([]string, 0, len(latest))
	for k := range latest {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	configured := e.Config.Command(hand)
	switch hand {
	case "test-proof":
		configured = e.Config.TestProofCommand()
	case "eyes", "review":
		configured = ""
	}
	for _, k := range keys {
		rec := latest[k]
		label := rec.ID
		if rec.Verdict != evidence.Pass {
			return fail("%s: verdict %s — %s", label, rec.Verdict, firstLine(rec.Summary))
		}
		if configured != "" {
			if ran, _ := rec.Data["command"].(string); ran != configured {
				return fail("%s ran %q but the configured command is %q: run `%s` again", label, ran, configured, HandCommand(hand))
			}
		}
		if hand == "eyes" {
			if src, _ := rec.Data["source"].(string); src != e.Config.EyesAdapter() {
				return fail("%s compared an image from %q, but eyes.adapter is %q: capture the real screen", label, src, e.Config.EyesAdapter())
			}
		}
		if err := log.Integrity(rec); err != nil {
			return fail("%v", err)
		}
		if stale := rec.Inputs.Stale(e.Now); len(stale) > 0 {
			return fail("%s is stale: changed since it ran: %s", label, short(stale))
		}
	}
	return pass("%s passed on the current content (%d record(s))", hand, len(keys))
}

func checkReview(e *Env, r config.Rule) Item {
	it := checkHand(e, r, "review")
	if it.Status != StatusPass {
		return it
	}
	rec := evidence.Latest(e.Evidence, "review")[""]
	if rec.Actor == "" {
		return fail("%s has no actor: reviews must be attributable", rec.ID)
	}
	workers := e.Workers()
	if workers[rec.Actor] {
		return fail("%s was written by %s, who worked on this task: the reviewer must be someone else", rec.ID, rec.Actor)
	}
	return pass("reviewed by %s, who did not work on the task", rec.Actor)
}

// Workers are everyone who built the task: the developer, whoever moved it to building, and
// every actor that ran a hand other than review.
func (e *Env) Workers() map[string]bool {
	w := map[string]bool{}
	if d := e.Task.Roles["developer"]; d != "" {
		w[d] = true
	}
	for _, ev := range e.Events {
		if ev.Type == "moved" && ev.To == board.Building || ev.Type == "edited" {
			w[ev.Actor] = true
		}
	}
	for _, rec := range e.Evidence {
		if rec.Hand != "review" && rec.Actor != "" {
			w[rec.Actor] = true
		}
	}
	return w
}

// gatedHands are the hands some gate depends on. Other runs (a diagnostic that fails on
// purpose, an exploratory command) never block delivery.
func gatedHands(cfg *config.Config) map[string]bool {
	h := map[string]bool{}
	for _, rules := range cfg.Gates {
		for _, r := range rules {
			switch r.Check {
			case "test_proof":
				h["test-proof"] = true
			case "eyes":
				h["eyes"] = true
			case "review_by_other_role":
				h["review"] = true
			case "hand_passed":
				h[str(r.With, "hand")] = true
			}
		}
	}
	return h
}

func checkAllFresh(e *Env, _ config.Rule) Item {
	gated := gatedHands(e.Config)
	var latest []evidence.Record
	for _, rec := range evidence.LatestAll(e.Evidence) {
		if gated[rec.Hand] {
			latest = append(latest, rec)
		}
	}
	if len(latest) == 0 {
		if len(gated) == 0 {
			return pass("no gate depends on evidence")
		}
		return fail("no evidence recorded")
	}
	log := evidence.Log{Store: e.Store, Task: e.Task.ID}
	for _, rec := range latest {
		if rec.Verdict != evidence.Pass {
			return fail("%s: latest %s verdict is %s — %s", rec.ID, rec.Hand, rec.Verdict, firstLine(rec.Summary))
		}
		if err := log.Integrity(rec); err != nil {
			return fail("%v", err)
		}
		if stale := rec.Inputs.Stale(e.Now); len(stale) > 0 {
			return fail("%s is stale: changed since it ran: %s", rec.ID, short(stale))
		}
	}
	return pass("%d evidence record(s) passed on the current content", len(latest))
}

// checkMatchesHead is evidence_fresh plus: none of the task's files has uncommitted changes,
// so the evidence describes what is committed, not what happens to be on disk.
func checkMatchesHead(e *Env, r config.Rule) Item {
	if it := checkAllFresh(e, r); it.Status != StatusPass {
		return it
	}
	dirty, err := e.Repo.Dirty(e.Changed)
	if err != nil {
		return fail("git status: %v", err)
	}
	if len(dirty) > 0 {
		return fail("uncommitted changes in task files: %s — commit them (the evidence must describe HEAD)", short(dirty))
	}
	return pass("evidence passed on exactly what HEAD contains")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

var conventionalRe = regexp.MustCompile(`^(feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert)(\([^)]+\))?!?: \S`)

func checkConventionalCommits(e *Env, _ config.Rule) Item {
	subjects, err := e.Repo.CommitSubjects(e.Base)
	if err != nil {
		return fail("git log: %v", err)
	}
	if len(subjects) == 0 {
		return fail("no commits since the task base")
	}
	for _, s := range subjects {
		if !conventionalRe.MatchString(s) {
			return fail("not a conventional commit: %q", s)
		}
	}
	return pass("%d conventional commit(s)", len(subjects))
}

// ConventionalSubject is exported for the commit hook.
func ConventionalSubject(s string) bool { return conventionalRe.MatchString(s) }

func short(list []string) string {
	if len(list) > 5 {
		return strings.Join(list[:5], ", ") + fmt.Sprintf(" and %d more", len(list)-5)
	}
	return strings.Join(list, ", ")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// HandCommand is the CLI command that produces evidence for a hand.
func HandCommand(hand string) string {
	switch hand {
	case "test-proof", "eyes":
		return "truehand hand " + hand
	case "review":
		return "truehand review --verdict pass|fail --notes review.md (by someone other than the developer)"
	}
	return "truehand hand run " + hand
}
