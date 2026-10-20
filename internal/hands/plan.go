package hands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/plan"
	"github.com/spaghetticoders/truehand/internal/tickets"
)

// FetchTicket is replaceable in tests.
var FetchTicket = func(ctx context.Context, adapter, id string) (*tickets.Ticket, error) {
	a, err := tickets.ForName(adapter)
	if err != nil {
		return nil, err
	}
	return a.Fetch(ctx, id)
}

type PlanContext struct {
	Ticket      *tickets.Ticket `json:"ticket"`
	Context     string          `json:"context"`
	Plan        string          `json:"plan"`
	PlanCreated bool            `json:"plan_created"`
}

// FetchPlanContext writes the ticket with all comments to context.md and creates the
// plan.md template if missing. Relative file refs resolve against the repo root.
func FetchPlanContext(ctx context.Context, ws *core.Workspace, t *board.Task, ref string) (*PlanContext, error) {
	if ref == "" {
		ref = t.Source
	}
	if ref == "" {
		return nil, errors.New("no ticket: pass one (e.g. linear:SPA-123, github:owner/repo#12, file:brief.md) or create the task with a source")
	}
	adapter, id := tickets.ParseRef(ref, ws.Config.Tickets.Adapter)
	if adapter == "file" && !filepath.IsAbs(id) {
		id = filepath.Join(ws.Repo.Root, id)
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tk, err := FetchTicket(cctx, adapter, id)
	if err != nil {
		return nil, err
	}
	ctxPath := filepath.Join(ws.Store.TaskDir(t.ID), "context.md")
	if err := os.WriteFile(ctxPath, []byte(tickets.Markdown(tk)), 0o644); err != nil {
		return nil, err
	}
	if t.Source == "" {
		t.Source = ref
		t.Updated = time.Now().UTC()
		if err := ws.Store.Save(t); err != nil {
			return nil, err
		}
	}
	pc := &PlanContext{Ticket: tk, Context: ws.Store.Rel(ctxPath), Plan: ws.Store.Rel(ws.Store.PlanPath(t.ID))}
	if _, err := os.Stat(ws.Store.PlanPath(t.ID)); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(ws.Store.PlanPath(t.ID), []byte(plan.Template(t.ID, t.Title, "context.md")), 0o644); err != nil {
			return nil, err
		}
		pc.PlanCreated = true
	}
	_ = ws.Store.AppendEvent(t.ID, board.Event{Type: "ticket_fetched", Actor: ws.Actor(), Detail: map[string]any{"ref": ref, "comments": len(tk.Comments)}})
	return pc, nil
}
