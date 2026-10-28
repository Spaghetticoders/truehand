package core

import (
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/evidence"
	"github.com/spaghetticoders/truehand/internal/plan"
)

// What a task or a session waits on the human for.
const (
	WaitPlanApproval    = "plan_approval"
	WaitAgentInput      = "agent_input"
	WaitPermission      = "permission"
	WaitCommentQuestion = "comment_question"
	WaitReadyForReview  = "ready_for_review"
	WaitToPush          = "to_push"
	WaitPROpen          = "pr_open"
	WaitBlocked         = "blocked"
	WaitStale           = "stale"
)

// Session statuses, as the agent manager reports them.
const (
	SessionStarting          = "starting"
	SessionWorking           = "working"
	SessionWaitingInput      = "waiting_input"
	SessionWaitingPermission = "waiting_permission"
	SessionIdle              = "idle"
	SessionExited            = "exited"
	SessionCrashed           = "crashed"
)

// SessionState is what the waiting list needs to know of an agent session.
type SessionState struct {
	ID     string
	Task   string
	Role   string
	Status string
	// ExitCode is set once the session ended.
	ExitCode *int
	Since    time.Time
}

func (s SessionState) live() bool {
	return s.Status != SessionExited && s.Status != SessionCrashed
}

// PushState is where a task's branch stands against its remote.
type PushState struct {
	HasUpstream bool
	Ahead       int
	PROpen      bool
}

// WaitingInput is one task as the waiting list sees it.
type WaitingInput struct {
	Task        BoardTask
	Plan        PlanView
	PlanUpdated time.Time
	Approvals   []board.Approval
	Events      []board.Event
	Push        PushState
}

type WaitingItem struct {
	Task    string    `json:"task,omitempty"`
	Kind    string    `json:"kind"`
	Since   time.Time `json:"since"`
	Session string    `json:"session,omitempty"`
}

// WaitingFor lists what waits on the human, newest first. Sessions whose task is not in
// tasks (free sessions) still count when they wait for input or permission.
func WaitingFor(tasks []WaitingInput, sessions []SessionState) []WaitingItem {
	items := []WaitingItem{}
	byTask := map[string][]SessionState{}
	for _, s := range sessions {
		switch s.Status {
		case SessionWaitingInput:
			items = append(items, WaitingItem{Task: s.Task, Kind: WaitAgentInput, Since: s.Since, Session: s.ID})
		case SessionWaitingPermission:
			items = append(items, WaitingItem{Task: s.Task, Kind: WaitPermission, Since: s.Since, Session: s.ID})
		}
		if s.Task != "" {
			byTask[s.Task] = append(byTask[s.Task], s)
		}
	}
	for _, in := range tasks {
		items = append(items, taskWaiting(in, byTask[in.Task.ID])...)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Since.After(items[j].Since) })
	return items
}

func taskWaiting(in WaitingInput, sessions []SessionState) []WaitingItem {
	t := in.Task
	id := t.ID
	var items []WaitingItem
	add := func(kind string, since time.Time, session string) {
		items = append(items, WaitingItem{Task: id, Kind: kind, Since: since, Session: session})
	}
	if t.State == board.Planned && planAwaitsApproval(in.Plan, in.Approvals) {
		add(WaitPlanApproval, orTime(in.PlanUpdated, t.Updated), "")
	}
	if c, ok := openQuestion(in.Events); ok && !t.Merged {
		add(WaitCommentQuestion, c.Time, "")
	}
	if t.State == board.Verified && !t.Stale && !hasLive(sessions, "review") {
		add(WaitReadyForReview, t.Updated, "")
	}
	if t.State == board.Delivered && !t.Merged {
		switch {
		case in.Push.PROpen:
			add(WaitPROpen, t.Updated, "")
		case !in.Push.HasUpstream || in.Push.Ahead > 0:
			add(WaitToPush, t.Updated, "")
		}
	}
	if t.Stale && !t.Merged {
		add(WaitStale, t.Updated, "")
	}
	if e, ok := closedGate(in.Events); ok && !t.Merged {
		add(WaitBlocked, e.Time, "")
	} else if s, ok := failedSession(sessions); ok && !t.Merged {
		add(WaitBlocked, s.Since, s.ID)
	}
	return items
}

// planAwaitsApproval mirrors the human_approval gate: a complete plan without an approval
// of its current content.
func planAwaitsApproval(p PlanView, apps []board.Approval) bool {
	if !p.Exists || !p.Started || len(p.Problems) > 0 {
		return false
	}
	for _, a := range apps {
		if a.State == board.Approved && a.PlanSHA == p.SHA256 {
			return false
		}
	}
	return true
}

// openQuestion is the latest comment when an agent wrote it and it asks something.
func openQuestion(events []board.Event) (board.Event, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Type != "comment" {
			continue
		}
		text, _ := e.Detail["text"].(string)
		return e, strings.HasPrefix(e.Actor, "claude:") && asks(text)
	}
	return board.Event{}, false
}

// asks reports whether a comment asks something: a line ending in "?" or starting with a
// question marker, once markdown (list and quote prefixes, emphasis) and trailing closers or
// emoji are set aside. An agent also comments to record what it decided, which needs no
// answer; a stray "?" in code is the cheaper mistake.
func asks(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		line = strings.NewReplacer("*", "", "_", "", "`", "").Replace(line)
		line = mdPrefixRe.ReplaceAllString(strings.TrimSpace(line), "")
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "q:") || strings.HasPrefix(lower, "domanda:") || strings.HasPrefix(lower, "question:") {
			return true
		}
		end := strings.TrimRightFunc(line, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '?'
		})
		if strings.HasSuffix(end, "?") {
			return true
		}
	}
	return false
}

var mdPrefixRe = regexp.MustCompile(`^(?:(?:[-+>#]|\d+[.)])\s*)+`)

// closedGate is the last refused move, unless the task moved since.
func closedGate(events []board.Event) (board.Event, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		switch events[i].Type {
		case "gate_closed":
			return events[i], true
		case "moved", "reworked", "created":
			return board.Event{}, false
		}
	}
	return board.Event{}, false
}

// failedSession is the task's latest session when it crashed or exited with an error.
func failedSession(sessions []SessionState) (SessionState, bool) {
	var last SessionState
	for _, s := range sessions {
		if last.ID == "" || s.Since.After(last.Since) {
			last = s
		}
	}
	failed := last.Status == SessionCrashed || last.Status == SessionExited && last.ExitCode != nil && *last.ExitCode != 0
	return last, last.ID != "" && failed
}

func hasLive(sessions []SessionState, role string) bool {
	for _, s := range sessions {
		if s.Role == role && s.live() {
			return true
		}
	}
	return false
}

func orTime(a, b time.Time) time.Time {
	if a.IsZero() {
		return b
	}
	return a
}

// PushStateOf reports whether branch has an upstream and how many commits it is ahead of it.
// Whether a PR is open is GitHub's to say; the caller fills it.
func (w *Workspace) PushStateOf(branch string) PushState {
	if branch == "" || strings.HasPrefix(branch, "-") {
		return PushState{}
	}
	if _, err := w.Repo.Git("rev-parse", "--verify", "-q", branch+"@{upstream}"); err != nil {
		return PushState{}
	}
	out, err := w.Repo.Git("rev-list", "--count", branch+"@{upstream}.."+branch)
	if err != nil {
		return PushState{HasUpstream: true}
	}
	n, _ := strconv.Atoi(strings.TrimSpace(out))
	return PushState{HasUpstream: true, Ahead: n}
}

// WaitingInputs gathers what WaitingFor needs from this checkout's board. Tasks read from
// other branches are read-only: their plan cannot be approved here, but they can be pushed.
func (w *Workspace) WaitingInputs(bv *BoardView) []WaitingInput {
	current, _ := w.Repo.Git("symbolic-ref", "--short", "-q", "HEAD")
	current = strings.TrimSpace(current)
	var ins []WaitingInput
	for _, bt := range bv.Tasks {
		in := WaitingInput{Task: bt}
		if bt.Branch != "" {
			if d, err := w.BranchDetail(bt.Branch, bt.ID); err == nil {
				in.Events = d.Events
			}
			in.Push = w.PushStateOf(bt.Branch)
		} else {
			if b, err := os.ReadFile(w.Store.PlanPath(bt.ID)); err == nil {
				probs := plan.Check(string(b))
				in.Plan = PlanView{Exists: true, SHA256: evidence.HashBytes(b), Problems: probs, Started: len(probs) < len(plan.Sections)}
			}
			if fi, err := os.Stat(w.Store.PlanPath(bt.ID)); err == nil {
				in.PlanUpdated = fi.ModTime().UTC()
			}
			in.Events, _ = w.Store.Events(bt.ID)
			in.Approvals, _ = w.Store.Approvals(bt.ID)
			in.Push = w.PushStateOf(current)
		}
		ins = append(ins, in)
	}
	return ins
}
