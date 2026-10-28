package core

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/plan"
)

var t0 = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

func at(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }

func task(id string, s board.State) BoardTask {
	return BoardTask{Task: &board.Task{ID: id, State: s, Updated: t0}, Missing: []string{}}
}

func kinds(items []WaitingItem) []string {
	out := []string{}
	for _, it := range items {
		out = append(out, it.Task+":"+it.Kind)
	}
	return out
}

func code(n int) *int { return &n }

func TestWaitingFor(t *testing.T) {
	pv := PlanView{Exists: true, Started: true, SHA256: "abc"}
	merged := task("M", board.Delivered)
	merged.Merged = true
	stale := task("S", board.Verified)
	stale.Stale = true
	cases := []struct {
		name     string
		in       WaitingInput
		sessions []SessionState
		want     []string
	}{
		{"complete plan, no approval", WaitingInput{Task: task("A", board.Planned), Plan: pv}, nil, []string{"A:plan_approval"}},
		{"plan approved as is", WaitingInput{Task: task("A", board.Planned), Plan: pv,
			Approvals: []board.Approval{{State: board.Approved, PlanSHA: "abc"}}}, nil, []string{}},
		{"plan changed after approval", WaitingInput{Task: task("A", board.Planned), Plan: pv,
			Approvals: []board.Approval{{State: board.Approved, PlanSHA: "old"}}}, nil, []string{"A:plan_approval"}},
		{"plan still the template", WaitingInput{Task: task("A", board.Planned), Plan: PlanView{Exists: true, SHA256: "abc"}}, nil, []string{}},
		{"plan incomplete", WaitingInput{Task: task("A", board.Planned), Plan: PlanView{Exists: true, Started: true, Problems: make([]plan.Problem, 1)}}, nil, []string{}},
		{"no plan", WaitingInput{Task: task("A", board.Planned)}, nil, []string{}},

		{"agent asked in comments", WaitingInput{Task: task("C", board.Building), Events: []board.Event{
			{Type: "comment", Actor: "human:h@x.y", Time: at(1)}, {Type: "comment", Actor: "claude:s1", Time: at(2), Detail: map[string]any{"text": "Keep the old API?\nI can do both."}}, {Type: "moved", Time: at(3)}}},
			nil, []string{"C:comment_question"}},
		{"agent only records decisions", WaitingInput{Task: task("C", board.Building), Events: []board.Event{
			{Type: "comment", Actor: "human:h@x.y", Time: at(1), Detail: map[string]any{"text": "Keep it?"}},
			{Type: "comment", Actor: "claude:s1", Time: at(2), Detail: map[string]any{"text": "Decided: keep the old API, as answered.\nNext: tests."}}}},
			nil, []string{}},
		{"question marker", WaitingInput{Task: task("C", board.Building), Events: []board.Event{
			{Type: "comment", Actor: "claude:s1", Time: at(2), Detail: map[string]any{"text": "Note.\nDomanda: quale colore usiamo per il badge"}}}},
			nil, []string{"C:comment_question"}},
		{"human answered", WaitingInput{Task: task("C", board.Building), Events: []board.Event{
			{Type: "comment", Actor: "claude:s1", Time: at(1)}, {Type: "comment", Actor: "human:h@x.y", Time: at(2)}}},
			nil, []string{}},

		{"verified, nobody reviewing", WaitingInput{Task: task("V", board.Verified)}, nil, []string{"V:ready_for_review"}},
		{"verified, review running", WaitingInput{Task: task("V", board.Verified)},
			[]SessionState{{ID: "r", Task: "V", Role: "review", Status: SessionWorking}}, []string{}},
		{"verified, review ended", WaitingInput{Task: task("V", board.Verified)},
			[]SessionState{{ID: "r", Task: "V", Role: "review", Status: SessionExited, ExitCode: code(0)}}, []string{"V:ready_for_review"}},

		{"delivered, never pushed", WaitingInput{Task: task("D", board.Delivered)}, nil, []string{"D:to_push"}},
		{"delivered, ahead", WaitingInput{Task: task("D", board.Delivered), Push: PushState{HasUpstream: true, Ahead: 2}}, nil, []string{"D:to_push"}},
		{"delivered, pushed", WaitingInput{Task: task("D", board.Delivered), Push: PushState{HasUpstream: true}}, nil, []string{}},
		{"delivered, PR open", WaitingInput{Task: task("D", board.Delivered), Push: PushState{HasUpstream: true, Ahead: 1, PROpen: true}}, nil, []string{"D:pr_open"}},

		{"merged is never stale, never to push", WaitingInput{Task: func() BoardTask { m := merged; m.Stale = true; return m }(),
			Events: []board.Event{{Type: "gate_closed", Time: at(1)}}},
			[]SessionState{{ID: "x", Task: "M", Status: SessionCrashed}}, []string{}},
		{"stale", WaitingInput{Task: stale}, nil, []string{"S:stale"}},

		{"gate refused", WaitingInput{Task: task("B", board.Building), Events: []board.Event{
			{Type: "moved", Time: at(1)}, {Type: "gate_closed", Time: at(2)}}}, nil, []string{"B:blocked"}},
		{"moved after the refusal", WaitingInput{Task: task("B", board.Verified), Events: []board.Event{
			{Type: "gate_closed", Time: at(1)}, {Type: "moved", Time: at(2)}}},
			[]SessionState{{ID: "r", Task: "B", Role: "review", Status: SessionIdle}}, []string{}},
		{"dev session crashed", WaitingInput{Task: task("B", board.Building)},
			[]SessionState{{ID: "old", Task: "B", Status: SessionExited, ExitCode: code(0), Since: at(1)}, {ID: "new", Task: "B", Status: SessionCrashed, Since: at(2)}},
			[]string{"B:blocked"}},
		{"failed session replaced by a working one", WaitingInput{Task: task("B", board.Building)},
			[]SessionState{{ID: "old", Task: "B", Status: SessionExited, ExitCode: code(1), Since: at(1)}, {ID: "new", Task: "B", Status: SessionWorking, Since: at(2)}},
			[]string{}},

		{"agent waits for input and permission, free session too", WaitingInput{Task: task("W", board.Building)},
			[]SessionState{{ID: "a", Task: "W", Status: SessionWaitingInput, Since: at(1)}, {ID: "f", Status: SessionWaitingPermission, Since: at(2)}},
			[]string{":permission", "W:agent_input"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := kinds(WaitingFor([]WaitingInput{c.in}, c.sessions)); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestWaitingForOrderAndSession(t *testing.T) {
	items := WaitingFor([]WaitingInput{
		{Task: task("OLD", board.Verified)},
		{Task: task("NEW", board.Building), Events: []board.Event{{Type: "comment", Actor: "claude:s", Time: at(5), Detail: map[string]any{"text": "Q: which?"}}}},
	}, []SessionState{{ID: "s1", Task: "NEW", Status: SessionWaitingPermission, Since: at(9)}})
	if got, want := kinds(items), []string{"NEW:permission", "NEW:comment_question", "OLD:ready_for_review"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if items[0].Session != "s1" || !items[1].Since.Equal(at(5)) {
		t.Fatalf("%+v", items)
	}
}

func TestWaitingInputs(t *testing.T) {
	dir, ws := repo(t)
	withOrigin(t, dir)
	if _, err := ws.NewTask("T-1", "Demo", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ws.Store.PlanPath("T-1"), []byte(goodPlan), 0o644); err != nil {
		t.Fatal(err)
	}
	ws.Comment("T-1", "plan ready?")
	bv, err := ws.Board()
	if err != nil {
		t.Fatal(err)
	}
	ins := ws.WaitingInputs(bv)
	if len(ins) != 1 || !ins[0].Plan.Exists || ins[0].Plan.SHA256 == "" || ins[0].PlanUpdated.IsZero() || len(ins[0].Events) != 2 {
		t.Fatalf("%+v", ins)
	}
	if !ins[0].Push.HasUpstream || ins[0].Push.Ahead != 0 {
		t.Fatalf("push %+v", ins[0].Push)
	}
	if got := kinds(WaitingFor(ins, nil)); !reflect.DeepEqual(got, []string{"T-1:plan_approval"}) {
		t.Fatalf("got %v", got)
	}
	ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: ins[0].Plan.SHA256})
	if got := kinds(WaitingFor(ws.WaitingInputs(bv), nil)); len(got) != 0 {
		t.Fatalf("approved plan still waiting: %v", got)
	}

	write(t, filepath.Join(dir, "x.txt"), "x")
	git(t, dir, "add", "x.txt")
	git(t, dir, "commit", "-q", "-m", "feat: x")
	if p := ws.PushStateOf("main"); p != (PushState{HasUpstream: true, Ahead: 1}) {
		t.Fatalf("push %+v", p)
	}
	git(t, dir, "branch", "lonely")
	if p := ws.PushStateOf("lonely"); p.HasUpstream {
		t.Fatalf("push %+v", p)
	}
	if p := ws.PushStateOf("--all"); p != (PushState{}) {
		t.Fatalf("option taken as a branch: %+v", p)
	}
}

const goodPlan = "# T\n\n## Goal\ng\n\n## Scope\ns\n\n## Out of scope\no\n\n## Steps\n1. a — verify: b\n\n## Verification\nv\n\n## Risks\nr\n"
