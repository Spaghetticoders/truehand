package hands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// FetchPlanContext writes context.md — the task's own description and comments, plus the
// linked external ticket when there is one — and creates the plan.md template if missing.
// Relative file refs resolve against the repo root.
func FetchPlanContext(ctx context.Context, ws *core.Workspace, t *board.Task, ref string) (*PlanContext, error) {
	if ref == "" {
		ref = t.Source
	}
	desc, err := ws.Store.Description(t.ID)
	if err != nil {
		return nil, err
	}
	events, err := ws.Store.Events(t.ID)
	if err != nil {
		return nil, err
	}
	var tk *tickets.Ticket
	if ref != "" {
		adapter, id := tickets.ParseRef(ref, ws.Config.Tickets.Adapter)
		if adapter == "" {
			return nil, fmt.Errorf("ticket %q has no tracker: write it as linear:%s, github:owner/repo#12 or file:brief.md, or set tickets.adapter in .truehand/config.yaml", ref, ref)
		}
		if adapter == "file" && !filepath.IsAbs(id) {
			id = filepath.Join(ws.Repo.Root, id)
		}
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if tk, err = FetchTicket(cctx, adapter, id); err != nil {
			return nil, err
		}
		tk.Ref = ref
	} else if strings.TrimSpace(desc) == "" {
		return nil, fmt.Errorf("task %s has no description: write what has to be done with `truehand task edit %s --description \"...\"` (or link an external ticket with --source)", t.ID, t.ID)
	}
	ctxPath := filepath.Join(ws.Store.TaskDir(t.ID), "context.md")
	if err := os.WriteFile(ctxPath, []byte(contextMarkdown(t, desc, events, tk)), 0o644); err != nil {
		return nil, err
	}
	if t.Source == "" && ref != "" {
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
	detail := map[string]any{"description": desc != ""}
	if tk != nil {
		detail["ref"] = ref
		detail["comments"] = len(tk.Comments)
	}
	_ = ws.Store.AppendEvent(t.ID, board.Event{Type: "context_fetched", Actor: ws.Actor(), Detail: detail})
	return pc, nil
}

func contextMarkdown(t *board.Task, desc string, events []board.Event, tk *tickets.Ticket) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — %s\n\n## Description\n\n", t.ID, t.Title)
	if d := strings.TrimSpace(desc); d != "" {
		b.WriteString(d + "\n\n")
	} else {
		b.WriteString("_(see the linked ticket below)_\n\n")
	}
	var comments []board.Event
	for _, e := range events {
		if e.Type == "comment" {
			comments = append(comments, e)
		}
	}
	if len(comments) > 0 {
		fmt.Fprintf(&b, "## Comments (%d)\n\n", len(comments))
		for _, c := range comments {
			text, _ := c.Detail["text"].(string)
			fmt.Fprintf(&b, "### %s — %s\n\n%s\n\n", c.Actor, c.Time.Local().Format("2006-01-02 15:04"), text)
		}
	}
	if tk != nil {
		b.WriteString("---\n\n## Linked ticket\n\n")
		md := tickets.Markdown(tk)
		b.WriteString(strings.ReplaceAll(strings.Replace(md, "# ", "### ", 1), "\n## ", "\n#### "))
	}
	return b.String()
}
