package cli

import (
	"strings"
	"testing"
)

// The review summary carries no actor id; task show names who produced each record, readably.
func TestTaskShowNamesEvidenceActors(t *testing.T) {
	s := bypassSandbox(t)
	t.Setenv("TRUEHAND_ACTOR", "")
	s.must("task", "move", "building")
	s.write("greet.txt", "hi\n")
	s.must("hand", "run", "check", "--quiet")
	s.must("task", "move", "verified")
	t.Setenv("TRUEHAND_ACTOR", "claude:81aae9d0-1111-2222-3333-444455556666")
	s.must("review", "--verdict", "pass", "--notes", writeNotes(t))
	t.Setenv("TRUEHAND_ACTOR", "")
	out := s.must("task", "show", "T-1")
	_, ev, _ := strings.Cut(out, "Evidence")
	if strings.Contains(ev, "review pass by") || strings.Contains(ev, "claude:81aae9d0-1111") {
		t.Fatalf("raw actor id shown:\n%s", out)
	}
	if !strings.Contains(out, "review pass  by 81aae9d0…") || !strings.Contains(out, "by Dev") {
		t.Fatalf("who produced the evidence is missing:\n%s", out)
	}
	if out := s.must("evidence", "list", "T-1"); !strings.Contains(out, "by 81aae9d0…") {
		t.Fatalf("evidence list:\n%s", out)
	}
}
