package serve

import (
	"testing"

	"github.com/spaghetticoders/truehand/internal/board"
)

func TestDecide(t *testing.T) {
	for _, c := range []struct {
		name           string
		f              taskFacts
		reason, action string
	}{
		{"inactive", taskFacts{ProjectInactive: true}, "", actActivate},
		{"read-only", taskFacts{State: board.Planned, PlanReady: true, ReadOnly: true}, "", ""},
		{"plan being written", taskFacts{State: board.Planned}, "", actStartAgent},
		{"plan being written, agent live", taskFacts{State: board.Planned, LiveDev: true}, "", actOpenAgent},
		{"plan ready", taskFacts{State: board.Planned, PlanReady: true}, waitApprovePlan, actApprovePlan},
		{"a ready plan beats the agent idle at its prompt", taskFacts{State: board.Planned, PlanReady: true, SessionWaiting: "input", LiveDev: true}, waitApprovePlan, actApprovePlan},
		{"a ready plan beats an open comment", taskFacts{State: board.Planned, PlanReady: true, OpenQuestion: true, LiveDev: true}, waitApprovePlan, actApprovePlan},
		{"delivered with the agent idle at its prompt: push", taskFacts{State: board.Delivered, SessionWaiting: "input", LiveDev: true}, waitPush, actPush},
		{"reviewed with the agent idle: deliver", taskFacts{State: board.Reviewed, DeliverGateOpen: true, SessionWaiting: "input", LiveDev: true}, waitDeliver, actDeliver},
		{"verified with the agent idle: review", taskFacts{State: board.Verified, SessionWaiting: "input", LiveDev: true}, waitReview, actStartReview},
		{"permission beats the plan", taskFacts{State: board.Planned, PlanReady: true, SessionWaiting: "permission", LiveDev: true}, waitPermission, actOpenAgent},
		{"question", taskFacts{State: board.Building, OpenQuestion: true, LiveDev: true}, waitQuestion, actOpenAgent},
		{"agent waits for input", taskFacts{State: board.Building, SessionWaiting: "input", LiveDev: true}, waitQuestion, actOpenAgent},
		{"approved", taskFacts{State: board.Approved}, "", actStartAgent},
		{"building live", taskFacts{State: board.Building, LiveDev: true}, "", actOpenAgent},
		{"building failing", taskFacts{State: board.Building, LiveDev: true, Failing: true}, waitFailing, actOpenAgent},
		{"building crashed", taskFacts{State: board.Building, ResumableDev: true, Failing: true}, waitFailing, actResumeAgent},
		{"building ended", taskFacts{State: board.Building, ResumableDev: true}, "", actResumeAgent},
		{"verified", taskFacts{State: board.Verified}, waitReview, actStartReview},
		{"verified, review live", taskFacts{State: board.Verified, LiveReview: true}, "", actOpenAgent},
		{"verified, review recorded: no second review", taskFacts{State: board.Verified, ReviewDone: true, ResumableDev: true}, "", actResumeAgent},
		{"reviewed, gate open", taskFacts{State: board.Reviewed, DeliverGateOpen: true}, waitDeliver, actDeliver},
		{"reviewed, gate closed", taskFacts{State: board.Reviewed, ResumableDev: true}, "", actResumeAgent},
		{"delivered, not pushed", taskFacts{State: board.Delivered}, waitPush, actPush},
		{"delivered, pushed", taskFacts{State: board.Delivered, Pushed: true}, waitPush, actOpenPR},
		{"delivered, PR open", taskFacts{State: board.Delivered, Pushed: true, PROpenOrMerged: true}, "", ""},
		{"merged", taskFacts{State: board.Delivered, Merged: true, Stale: true}, "", ""},
		{"stale", taskFacts{State: board.Verified, Stale: true}, waitStale, actSendBack},
		{"failing beats stale", taskFacts{State: board.Building, Stale: true, Failing: true, LiveDev: true}, waitFailing, actOpenAgent},
	} {
		r, a := decide(c.f)
		if r != c.reason || a != c.action {
			t.Errorf("%s: got %q/%q, want %q/%q", c.name, r, a, c.reason, c.action)
		}
	}
}
