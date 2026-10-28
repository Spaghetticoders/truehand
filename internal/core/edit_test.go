package core

import (
	"reflect"
	"testing"
)

func ptr(s string) *string { return &s }

func TestEdit(t *testing.T) {
	_, ws := repo(t)
	if _, err := ws.NewTask("T-1", "Old", "linear:SPA-1"); err != nil {
		t.Fatal(err)
	}
	task, changed, err := ws.Edit("T-1", TaskEdit{Title: ptr("New"), Description: ptr("# Do it\n")})
	if err != nil {
		t.Fatal(err)
	}
	if task.Title != "New" || !reflect.DeepEqual(changed, []string{"description", "title"}) {
		t.Fatalf("title %q changed %v", task.Title, changed)
	}
	if d, _ := ws.Store.Description("T-1"); d != "# Do it\n" {
		t.Fatalf("description %q", d)
	}
	loaded, _ := ws.Store.Load("T-1")
	if loaded.Title != "New" || loaded.Source != "linear:SPA-1" {
		t.Fatalf("saved %+v", loaded)
	}

	if _, changed, _ = ws.Edit("T-1", TaskEdit{Source: ptr("")}); !reflect.DeepEqual(changed, []string{"source"}) {
		t.Fatalf("changed %v", changed)
	}
	if loaded, _ = ws.Store.Load("T-1"); loaded.Source != "" {
		t.Fatalf("source not removed: %q", loaded.Source)
	}

	if _, changed, _ = ws.Edit("T-1", TaskEdit{Title: ptr("New")}); len(changed) != 0 {
		t.Fatalf("same title counted as a change: %v", changed)
	}
	events, _ := ws.Store.Events("T-1")
	edits := 0
	for _, e := range events {
		if e.Type == "edited" {
			edits++
			if e.Actor != "human:h@x.y" {
				t.Fatalf("edited by %q", e.Actor)
			}
		}
	}
	if edits != 2 {
		t.Fatalf("%d edited events, want 2", edits)
	}

	if _, _, err := ws.Edit("T-1", TaskEdit{Title: ptr("  ")}); err == nil {
		t.Fatal("empty title accepted")
	}
	if _, _, err := ws.Edit("T-9", TaskEdit{Title: ptr("x")}); err == nil {
		t.Fatal("unknown task accepted")
	}
}
