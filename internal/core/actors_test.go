package core

import (
	"reflect"
	"testing"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/evidence"
)

func TestWorkers(t *testing.T) {
	_, ws := repo(t)
	task, err := ws.NewTask("T-1", "Demo", "")
	if err != nil {
		t.Fatal(err)
	}
	task.Roles = map[string]string{"developer": "claude:dev"}
	ws.Store.Save(task)
	ws.Store.AppendEvent("T-1", board.Event{Type: "moved", Actor: "claude:mover", To: board.Building})
	ws.Store.AppendEvent("T-1", board.Event{Type: "moved", Actor: "claude:verifier", To: board.Verified})
	ws.Store.AppendEvent("T-1", board.Event{Type: "edited", Actor: "human:editor@x.y"})
	ws.Store.AppendEvent("T-1", board.Event{Type: "comment", Actor: "human:commenter@x.y"})
	log := evidence.Log{Store: ws.Store, Task: "T-1"}
	for _, r := range []evidence.Record{{ID: "a", Hand: "test-proof", Actor: "claude:tester"}, {ID: "b", Hand: "review", Actor: "claude:reviewer"}} {
		if err := log.Append(&r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ws.Workers(task)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"claude:dev": true, "claude:mover": true, "human:editor@x.y": true, "claude:tester": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("workers = %v, want %v", got, want)
	}
}

func TestResolveActors(t *testing.T) {
	sessions := []SessionRef{{ClaudeSessionID: "5f0c2a9e-1111-2222-3333-444455556666", Role: "review", Task: "T-12"}}
	got := ResolveActors([]string{
		"human:h@x.y", "human:other@x.y", "claude:5f0c2a9e-1111-2222-3333-444455556666",
		"claude:aaaa0000-unknown-session", "claude:pid-4242", "ci:github-actions-runner-01", "approver:mac", "",
	}, sessions, Identity{Name: "Human Name", Email: "H@X.Y"})
	want := map[string]ActorInfo{
		"human:h@x.y":     {Name: "Human Name", Kind: ActorHuman},
		"human:other@x.y": {Name: "other@x.y", Kind: ActorHuman},
		"claude:5f0c2a9e-1111-2222-3333-444455556666": {Name: "5f0c2a9e…", Kind: ActorAgent, Role: "review", Task: "T-12"},
		"claude:aaaa0000-unknown-session":             {Name: "aaaa0000…", Kind: ActorAgent},
		"claude:pid-4242":                             {Name: "pid-4242", Kind: ActorAgentUnbound},
		"ci:github-actions-runner-01":                 {Name: "ci:githu…", Kind: ActorUnknown},
		"approver:mac":                                {Name: "mac", Kind: ActorHuman},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
}

func TestActorsOf(t *testing.T) {
	d := &TaskDetail{
		TaskView: &TaskView{
			Task:     &board.Task{Roles: map[string]string{"developer": "claude:dev"}},
			Comments: []board.Event{{Actor: "human:a@x.y"}},
			Evidence: []EvidenceView{{Record: evidence.Record{Actor: "claude:t"}}, {Record: evidence.Record{}}},
		},
		Events: []board.Event{{Actor: "human:a@x.y"}, {Actor: "claude:m"}},
	}
	if got, want := ActorsOf(d), []string{"claude:dev", "claude:m", "claude:t", "human:a@x.y"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestResolveActorsStripsControls(t *testing.T) {
	got := ResolveActors([]string{"human:\x1b]0;pwned\x07ev\u202eil", "ci:\x1b[2Jèèèèèèèèèèèè"}, nil, Identity{})
	if n := got["human:\x1b]0;pwned\x07ev\u202eil"].Name; n != "]0;pwnedevil" {
		t.Fatalf("human name %q", n)
	}
	if n := got["ci:\x1b[2Jèèèèèèèèèèèè"].Name; n != "ci:[2Jèè…" {
		t.Fatalf("unknown name %q", n)
	}
	if p := Printable("ok\x1b[31m\u200bred\tx"); p != "ok[31mredx" {
		t.Fatalf("printable %q", p)
	}
}
