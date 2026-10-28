package serve

import "github.com/spaghetticoders/truehand/internal/board"

// What the human is waited on for, and the one button a task shows. The UI translates both.
const (
	waitApprovePlan = "approvePlan"
	waitQuestion    = "question"
	waitPermission  = "permission"
	waitReview      = "review"
	waitDeliver     = "deliver"
	waitPush        = "push"
	waitFailing     = "failing"
	waitStale       = "stale"

	actActivate    = "activate"
	actStartAgent  = "start-agent"
	actApprovePlan = "approve-plan"
	actStartReview = "start-review"
	actDeliver     = "deliver"
	actPush        = "push"
	actOpenPR      = "open-pr"
	actOpenAgent   = "open-agent"
	actSendBack    = "send-back"
	actResumeAgent = "resume-agent"
)

// waitPriority orders the reasons when several hold: the first one wins.
var waitPriority = []string{waitPermission, waitApprovePlan, waitQuestion, waitFailing, waitStale, waitReview, waitDeliver, waitPush, waitIdle}

// waitIdle is an agent that finished its turn and waits at its prompt: it is waiting for
// the human only when nothing else is (it always stops there after the plan or the commit).
const waitIdle = "idle"

// taskFacts is everything the decision depends on.
type taskFacts struct {
	State           board.State
	ReadOnly        bool
	Merged          bool
	Stale           bool
	PlanReady       bool
	OpenQuestion    bool
	Failing         bool
	SessionWaiting  string // "input" | "permission" from a live session of the task
	LiveDev         bool
	LiveReview      bool
	ResumableDev    bool
	DeliverGateOpen bool
	Pushed          bool
	PROpenOrMerged  bool
	ProjectInactive bool
	// ReviewDone: the review is recorded and the reviewed gate is open.
	ReviewDone bool
}

// decide implements the contract's table: the reason the task waits on the human (if any)
// and its primary action, which serves that reason when there is one.
func decide(f taskFacts) (reason, action string) {
	if f.ProjectInactive {
		return "", actActivate
	}
	if f.ReadOnly {
		return "", ""
	}
	held := map[string]bool{
		waitPermission:  f.SessionWaiting == "permission",
		waitQuestion:    f.OpenQuestion,
		waitIdle:        f.SessionWaiting == "input",
		waitApprovePlan: f.State == board.Planned && f.PlanReady,
		waitFailing:     f.Failing && !f.Merged,
		waitStale:       f.Stale && !f.Merged,
		waitReview:      f.State == board.Verified && !f.LiveReview && !f.ReviewDone,
		waitDeliver:     f.State == board.Reviewed && f.DeliverGateOpen,
		waitPush:        f.State == board.Delivered && !f.Merged && !f.PROpenOrMerged,
	}
	for _, r := range waitPriority {
		if held[r] {
			reason = r
			break
		}
	}
	switch reason {
	case waitIdle:
		// The contract's reason for an agent waiting at its prompt.
		return waitQuestion, actOpenAgent
	case waitPermission, waitQuestion:
		return reason, actOpenAgent
	case waitApprovePlan:
		return reason, actApprovePlan
	case waitStale:
		return reason, actSendBack
	case waitReview:
		return reason, actStartReview
	case waitDeliver:
		return reason, actDeliver
	case waitPush:
		if f.Pushed {
			return reason, actOpenPR
		}
		return reason, actPush
	}
	return reason, stateAction(f)
}

// stateAction is the button when nothing waits on the human, or when a failure is the
// agent's to fix.
func stateAction(f taskFacts) string {
	agent := func() string {
		switch {
		case f.LiveDev:
			return actOpenAgent
		case f.ResumableDev:
			return actResumeAgent
		}
		return actStartAgent
	}
	switch f.State {
	case board.Planned, board.Approved, board.Building:
		return agent()
	case board.Verified:
		if f.LiveReview {
			return actOpenAgent
		}
		if f.ReviewDone {
			// Moving to reviewed is the reviewer's (or the developer's) step, not a new review.
			return agent()
		}
		return actStartReview
	case board.Reviewed:
		return agent()
	}
	return ""
}
