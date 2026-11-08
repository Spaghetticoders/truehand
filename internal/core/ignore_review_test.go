package core

import (
	"testing"

	"github.com/spaghetticoders/truehand/internal/board"
)

// The list of files ignored at a stack point counts only as the human accepted it: the
// last restack event, by a human, for this base, this predecessor and this exact list.
func TestReviewSignedIgnoreIsExactlyWhatTheHumanAccepted(t *testing.T) {
	_, ws := repo(t)
	list := []string{"Podfile.lock", "tool/x.bin"}
	base := "1111111111111111111111111111111111111111"
	mk := func(id string, ignore []string, evs ...board.Event) *board.Task {
		task := &board.Task{ID: id, Title: "x", State: board.Approved, Base: base, BaseRef: "main", StackedOn: "COACH-1", StackedIgnore: ignore}
		if err := ws.Store.Create(task); err != nil {
			t.Fatal(err)
		}
		for _, e := range evs {
			ws.Store.AppendEvent(id, e)
		}
		return task
	}
	human := func(b, on string, l []string) board.Event {
		return board.Event{Type: "restacked", Actor: "human:h@x.y", Detail: map[string]any{"base": b, "stacked_on": on, "ignore_hash": IgnoreHash(l)}}
	}
	if got := ws.signedIgnore(mk("OK", list, human(base, "COACH-1", list))); len(got) != 2 {
		t.Fatalf("the human's list: %v", got)
	}
	for name, task := range map[string]*board.Task{
		"list extended after":      mk("MORE", append(append([]string{}, list...), "lib/a_fix.txt"), human(base, "COACH-1", list)),
		"accepted at another base": mk("BASE", list, human("2222222222222222222222222222222222222222", "COACH-1", list)),
		"accepted on another task": mk("PRED", list, human(base, "COACH-9", list)),
		"an agent restacked after": mk("LATER", list, human(base, "COACH-1", list), board.Event{Type: "restacked", Actor: "claude:agent", Detail: map[string]any{"base": base, "stacked_on": "COACH-1", "ignore_hash": IgnoreHash(list)}}),
	} {
		if got := ws.signedIgnore(task); got != nil {
			t.Errorf("%s: ignore list %v honoured", name, got)
		}
	}
}
