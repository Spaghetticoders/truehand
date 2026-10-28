package core

import (
	"errors"
	"strings"
	"time"

	"github.com/spaghetticoders/truehand/internal/board"
)

// TaskEdit names the fields to change; nil leaves a field as it is. An empty Source removes
// the link to the external ticket.
type TaskEdit struct {
	Title       *string
	Description *string
	Source      *string
}

// Edit changes a task's title, description or ticket and logs which fields changed. Editing
// counts as working on the task: the editor may not review it.
func (w *Workspace) Edit(id string, e TaskEdit) (*board.Task, []string, error) {
	if e.Title != nil && strings.TrimSpace(*e.Title) == "" {
		return nil, nil, errors.New("empty title")
	}
	t, err := w.Store.Load(id)
	if err != nil {
		return nil, nil, err
	}
	changed := []string{}
	if e.Description != nil {
		if err := w.Store.SetDescription(t.ID, *e.Description); err != nil {
			return nil, nil, err
		}
		changed = append(changed, "description")
	}
	if e.Title != nil && *e.Title != t.Title {
		t.Title = *e.Title
		changed = append(changed, "title")
	}
	if e.Source != nil {
		t.Source = *e.Source
		changed = append(changed, "source")
	}
	if len(changed) > 0 {
		t.Updated = time.Now().UTC()
		if err := w.Store.Save(t); err != nil {
			return nil, nil, err
		}
		_ = w.Store.AppendEvent(t.ID, board.Event{Type: "edited", Actor: w.Actor(), Detail: map[string]any{"fields": changed}})
	}
	return t, changed, nil
}
