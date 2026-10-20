package cli

import (
	"context"
	"flag"
	"fmt"

	"github.com/spaghetticoders/truehand/internal/hands"
)

func init() {
	register("plan fetch", "Fetch the task's ticket and comments into context.md, and create plan.md", cmdPlanFetch)
}

func cmdPlanFetch(c *ctx, args []string) error {
	fs := flag.NewFlagSet("plan fetch", flag.ContinueOnError)
	task := fs.String("task", "", "task id (default: active task)")
	asJSON := fs.Bool("json", false, "JSON output")
	pos, _, err := parse(fs, args)
	if err != nil {
		return err
	}
	ws, err := c.workspace()
	if err != nil {
		return err
	}
	t, err := ws.ResolveTask(*task)
	if err != nil {
		return err
	}
	pc, err := hands.FetchPlanContext(context.Background(), ws, t, first(pos))
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(c.stdout, pc)
	}
	if pc.Ticket != nil {
		fmt.Fprintf(c.stdout, "Fetched %s — %s (%d comment(s))\n", pc.Ticket.Ref, pc.Ticket.Title, len(pc.Ticket.Comments))
	}
	fmt.Fprintf(c.stdout, "Context → %s\n", pc.Context)
	if pc.PlanCreated {
		fmt.Fprintf(c.stdout, "Wrote %s: read context.md (comments included), fill every section, then `truehand plan check`\n", pc.Plan)
	}
	return nil
}
