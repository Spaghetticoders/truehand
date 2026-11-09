package board

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An older binary saving a task keeps the fields a newer one wrote; fields it knows and
// clears stay cleared.
func TestSaveKeepsUnknownFields(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	task := &Task{ID: "T-1", Title: "x", State: Planned, StackedOn: "T-0"}
	if err := s.Create(task); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(s.TaskDir("T-1"), "task.yaml")
	b, _ := os.ReadFile(p)
	os.WriteFile(p, append(b, []byte("future_field:\n    - a\n    - b\n")...), 0o644)
	got, err := s.Load("T-1")
	if err != nil {
		t.Fatal(err)
	}
	got.State, got.StackedOn = Building, ""
	if err := s.Save(got); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(p)
	if !strings.Contains(string(b), "future_field:") || !strings.Contains(string(b), "- b") {
		t.Fatalf("unknown field dropped:\n%s", b)
	}
	if strings.Contains(string(b), "stacked_on") || !strings.Contains(string(b), "state: building") {
		t.Fatalf("known fields not as saved:\n%s", b)
	}
	if again, err := s.Load("T-1"); err != nil || again.State != Building {
		t.Fatalf("reload: %+v %v", again, err)
	}
}
