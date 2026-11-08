// Package gate evaluates the rules that guard entering a state.
package gate

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/config"
	"github.com/spaghetticoders/truehand/internal/evidence"
	"github.com/spaghetticoders/truehand/internal/gitx"
	"github.com/spaghetticoders/truehand/internal/glob"
	"github.com/spaghetticoders/truehand/internal/plan"
	"github.com/spaghetticoders/truehand/internal/sign"
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
	// ApproversRef is the commit whose config lists the approvers; empty: the task's base ref.
	ApproversRef string
	// Approvers, when set, replaces BaseApprovers (the caller remembers it across tasks).
	Approvers func() ([]config.Approver, error)
	// AtPoint: Now is the task at a commit (another task is stacked on it), not the working
	// tree; every input of its evidence must match there, but those in Ignore.
	AtPoint bool
	// Ignore, at a point, lists the inputs a human accepted to ignore there: files the working
	// tree held when a hand ran that are not in the task's commits (see core.RestackWith).
	Ignore []string
}

// inputs is what of rec's inputs the freshness of the task's evidence depends on.
func (e *Env) inputs(rec evidence.Record) evidence.Fingerprint {
	if !e.AtPoint || len(e.Ignore) == 0 {
		return rec.Inputs
	}
	return rec.Inputs.Only(func(p string) bool { return !slices.Contains(e.Ignore, p) })
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
	return newEnv(repo, cfg, store, t, base, fp, recs)
}

// NewEnvAt is NewEnv with the fingerprint of the task's files already computed on base.
func NewEnvAt(repo *gitx.Repo, cfg *config.Config, store *board.Store, t *board.Task, base string, fp evidence.Fingerprint) (*Env, error) {
	recs, err := evidence.Log{Store: store, Task: t.ID}.Records()
	if err != nil {
		return nil, err
	}
	return newEnv(repo, cfg, store, t, base, fp, recs)
}

func newEnv(repo *gitx.Repo, cfg *config.Config, store *board.Store, t *board.Task, base string, fp evidence.Fingerprint, recs []evidence.Record) (*Env, error) {
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
	var approvers []config.Approver
	if e.Approvers != nil {
		approvers, err = e.Approvers()
	} else {
		approvers, err = BaseApprovers(e.Repo, e.Config, e.Task, e.ApproversRef)
	}
	if err != nil {
		return fail("approvers: %v", err)
	}
	if len(approvers) > 0 {
		return checkSignedApproval(e, approvers, apps, sha)
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

// checkSignedApproval needs an approval of this plan signed by a key the base branch lists,
// for this repo and task, with a nonce no earlier valid approval of the task used: a copy of a
// signed approval counts as nothing, and neither a copy nor a forged record voids the original.
func checkSignedApproval(e *Env, approvers []config.Approver, apps []board.Approval, sha string) Item {
	repoID := RepoID(e.Repo)
	used := map[string]bool{}
	var found *config.Approver
	var foundSig *sign.Signed
	var last error
	stale := false
	for _, a := range apps {
		if a.State != board.Approved {
			continue
		}
		ap, err := VerifySignedApproval(repoID, approvers, e.Task.ID, a.PlanSHA, a.Sig)
		if err == nil && used[a.Sig.Intent.Nonce] {
			err = fmt.Errorf("nonce %s was already used by an earlier approval", a.Sig.Intent.Nonce)
		}
		if err == nil {
			used[a.Sig.Intent.Nonce] = true
		}
		if a.PlanSHA != sha {
			stale = true
			continue
		}
		if err != nil {
			last = err
			continue
		}
		found, foundSig = ap, a.Sig
	}
	if found != nil {
		return pass("approved by %s, signed with key %s (%s)", found.Name, found.ID, foundSig.Format)
	}
	if last != nil {
		return fail("the approval of this plan.md is not validly signed by an approver: %v", last)
	}
	if stale {
		return fail("plan.md changed after the last approval: an approver must sign the new plan again")
	}
	return fail("waiting for an approver: sign the approval of plan.md in the Truehand app (approvers are listed on the base branch)")
}

// VerifySignedApproval checks that s approves exactly this plan of this task in this repo and
// carries a valid signature by one of approvers. It does not check the nonce's uniqueness.
func VerifySignedApproval(repoID string, approvers []config.Approver, taskID, planSHA string, s *sign.Signed) (*config.Approver, error) {
	return VerifySigned(repoID, approvers, sign.ActionApprovePlan, taskID, planSHA, "", s)
}

// VerifySigned checks that s authorises action on exactly this task, plan and head (empty
// for approve-plan) in this repo, signed by one of approvers. Freshness and nonce reuse are
// the caller's to check.
func VerifySigned(repoID string, approvers []config.Approver, action, taskID, planSHA, head string, s *sign.Signed) (*config.Approver, error) {
	if s == nil {
		return nil, errors.New("not signed")
	}
	in := s.Intent
	switch {
	case in.Action != action:
		return nil, fmt.Errorf("signed for %q, not %s", in.Action, action)
	case in.Head != head:
		return nil, errors.New("signed for another commit")
	case in.Task != taskID:
		return nil, fmt.Errorf("signed for task %q", in.Task)
	case in.PlanSHA256 != planSHA:
		return nil, errors.New("signed for another version of plan.md")
	case in.Repo != repoID:
		return nil, fmt.Errorf("signed for repo %q, this is %q", in.Repo, repoID)
	}
	var ap *config.Approver
	for i := range approvers {
		if approvers[i].ID == in.Approver {
			if ap != nil {
				return nil, fmt.Errorf("approver id %q is listed twice", in.Approver)
			}
			ap = &approvers[i]
		}
	}
	if ap == nil {
		return nil, fmt.Errorf("%q is not an approver on the base branch", in.Approver)
	}
	pub, err := sign.ParsePublicKey(ap.PublicKey)
	if err != nil {
		return nil, err
	}
	if err := s.Verify(pub, ap.RPID); err != nil {
		return nil, err
	}
	return ap, nil
}

// RepoID names the repo in signed intents: its origin URL, normalized, or without an origin
// its root commit, so two repos never accept each other's signatures.
func RepoID(repo *gitx.Repo) string {
	if u := sign.NormalizeRepoURL(repo.ConfigValue("remote.origin.url")); u != "" {
		return u
	}
	out, err := repo.Git("rev-list", "--max-parents=0", "HEAD")
	if err != nil {
		return ""
	}
	roots := strings.Fields(out)
	if len(roots) == 0 {
		return ""
	}
	sort.Strings(roots)
	return "root:" + roots[0]
}

// BaseApprovers returns the approvers whose keys count for a task; none means unsigned
// approvals still count. Approvers come only from committed configs, never from the working
// tree: a key the working tree adds could be an agent's own.
//
// In CI ref is the tip of the base branch the change goes into, and its list is the only one:
// what a branch says is ignored, and a revocation on the base holds at once, also for branches
// made before it. Locally the sources are the task's base ref, the configured base, the commit
// the task started from, origin's default branch, local main or master; of those that list
// approvers only the tips count (a source another descends from is an older state of the same
// history), so a rotated key or a removed approver does not come back through an older ref.
// Tips on diverged histories count together, an id taking its key from the first in the order
// above. A source that lists no approvers never hides the others (so removing every approver
// in a commit leaves the older lists in force locally: the strict direction). Once any source
// lists approvers, a shallow clone or info/grafts is an error; without approvers nothing
// changes.
// A source that does not resolve is skipped; it is an error only when none of them
// resolves. Local refs are the clone's to move, so only CI's check is a real lock.
func BaseApprovers(repo *gitx.Repo, cfg *config.Config, t *board.Task, ref string) ([]config.Approver, error) {
	if ref != "" {
		list, err := approversAt(repo, ref)
		if err == nil && len(list) > 0 {
			if why := repo.HistoryRewritten(); why != "" {
				return nil, fmt.Errorf("approvers are listed, so signatures count, but the history is not the real one: %s", why)
			}
		}
		return list, err
	}
	anchors := []string{t.BaseRef, cfg.Base, t.Base, "refs/remotes/origin/HEAD", "origin/main", "origin/master", "refs/heads/main", "refs/heads/master"}
	commits, err := resolveCommits(repo, anchors)
	if err != nil {
		return nil, err
	}
	type source struct {
		commit string
		list   []config.Approver
	}
	var sources []source
	resolved := false
	for _, c := range commits {
		if c == "" {
			continue
		}
		resolved = true
		list, err := approversAtCommit(repo, c)
		if err != nil {
			return nil, err
		}
		if len(list) > 0 {
			sources = append(sources, source{c, list})
		}
	}
	if !resolved {
		return nil, fmt.Errorf("none of the task's base refs (%s, %s) nor main, master or origin's resolves: cannot tell whether approvers are listed", orDash(t.BaseRef), orDash(cfg.Base))
	}
	if len(sources) > 0 {
		if why := repo.HistoryRewritten(); why != "" {
			return nil, fmt.Errorf("approvers are listed, so signatures count, but the history is not the real one: %s", why)
		}
	}
	// Only the tips count: a source another one descends from is an older state of the same
	// history, and what was rotated or removed since must not come back through it.
	var keys []config.Approver
	seen := map[string]bool{}
	for i, src := range sources {
		older := false
		for j := range sources {
			if j != i && sources[j].commit != src.commit && isAncestor(repo, src.commit, sources[j].commit) {
				older = true
				break
			}
		}
		if older {
			continue
		}
		for _, a := range src.list {
			if !seen[a.ID] {
				seen[a.ID] = true
				keys = append(keys, a)
			}
		}
	}
	return keys, nil
}

// noReplace makes git read the real objects: a replace ref changes what local commands see
// under a commit id, not what a push sends or what CI checks out.
const noReplace = "--no-replace-objects"

// Ancestry between two commits never changes, so it is asked once per process.
var ancestorCache sync.Map

// isAncestor reports whether a is an ancestor of b.
func isAncestor(repo *gitx.Repo, a, b string) bool {
	k := a + b
	if v, ok := ancestorCache.Load(k); ok {
		return v.(bool)
	}
	_, err := repo.Git(noReplace, "merge-base", "--is-ancestor", a, b)
	ancestorCache.Store(k, err == nil)
	return err == nil
}

// resolveCommits resolves refs to commit ids in one git call; "" for a ref that is empty, an
// option or not a commit.
func resolveCommits(repo *gitx.Repo, refs []string) ([]string, error) {
	var in strings.Builder
	for _, r := range refs {
		if r == "" || r == gitx.EmptyTree || strings.HasPrefix(r, "-") || strings.ContainsAny(r, "\n\r") {
			r = "-"
		}
		in.WriteString(r + "^{commit}\n")
	}
	out, err := repo.GitInput([]byte(in.String()), noReplace, "cat-file", "--batch-check=%(objectname)")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != len(refs) {
		return nil, fmt.Errorf("git cat-file: %d answers for %d refs", len(lines), len(refs))
	}
	commits := make([]string, len(refs))
	for i, l := range lines {
		if sha1Re.MatchString(l) {
			commits[i] = l
		}
	}
	return commits, nil
}

var sha1Re = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// The approvers a commit lists never change, so they are read once per process.
var approversCache sync.Map

func approversAtCommit(repo *gitx.Repo, commit string) ([]config.Approver, error) {
	if v, ok := approversCache.Load(commit); ok {
		return slices.Clone(v.([]config.Approver)), nil
	}
	list, err := approversAt(repo, commit)
	if err != nil {
		return nil, err
	}
	approversCache.Store(commit, slices.Clone(list))
	return list, nil
}

// CommittedApprovers returns the approvers the config committed at commit lists.
func CommittedApprovers(repo *gitx.Repo, commit string) ([]config.Approver, error) {
	c, err := resolveCommits(repo, []string{commit})
	if err != nil {
		return nil, err
	}
	if c[0] == "" {
		return nil, fmt.Errorf("%q is not a commit", commit)
	}
	return approversAtCommit(repo, c[0])
}

var errRefNotFound = errors.New("ref not found")

// approversAt reads the approvers of the config committed at ref; none when it has no config.
func approversAt(repo *gitx.Repo, ref string) ([]config.Approver, error) {
	if strings.HasPrefix(ref, "-") {
		return nil, fmt.Errorf("invalid ref %q", ref)
	}
	commit, err := repo.Git(noReplace, "rev-parse", "--verify", "-q", ref+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("base ref %s: %w (in CI, fetch it)", ref, errRefNotFound)
	}
	obj := strings.TrimSpace(commit) + ":.truehand/config.yaml"
	if _, err := repo.Git(noReplace, "cat-file", "-e", obj); err != nil {
		return nil, nil
	}
	out, err := repo.Git(noReplace, "cat-file", "blob", obj)
	if err != nil {
		return nil, err
	}
	c, err := config.Parse([]byte(out))
	if err != nil {
		return nil, fmt.Errorf("config on %s: %w", ref, err)
	}
	return c.Approvers, nil
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
		if stale := e.inputs(rec).Stale(e.Now); len(stale) > 0 {
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

func (e *Env) Workers() map[string]bool { return Workers(e.Task, e.Events, e.Evidence) }

// Workers are everyone who built the task: the developer, whoever moved it to building or
// edited it, and every actor that ran a hand other than review.
func Workers(t *board.Task, events []board.Event, recs []evidence.Record) map[string]bool {
	w := map[string]bool{}
	if d := t.Roles["developer"]; d != "" {
		w[d] = true
	}
	for _, ev := range events {
		if ev.Type == "moved" && ev.To == board.Building || ev.Type == "edited" {
			w[ev.Actor] = true
		}
	}
	for _, rec := range recs {
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
		if stale := e.inputs(rec).Stale(e.Now); len(stale) > 0 {
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
