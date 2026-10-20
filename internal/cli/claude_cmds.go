package cli

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/spaghetticoders/truehand/internal/claudeplugin"
)

func init() {
	register("claude install", "Install or refresh the Truehand plugin (hooks, MCP, skills) in Claude Code", cmdClaudeInstall)
	register("claude status", "Report whether the Truehand plugin is installed in Claude Code", cmdClaudeStatus)
}

func cmdClaudeInstall(c *ctx, args []string) error {
	fs := flag.NewFlagSet("claude install", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "JSON output")
	if _, _, err := parse(fs, args); err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dir, err := claudeplugin.Install(cctx)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(c.stdout, map[string]any{"installed": true, "plugin": claudeplugin.PluginID, "marketplace": dir})
	}
	fmt.Fprintf(c.stdout, "Installed %s from %s. New Claude Code sessions load its hooks, MCP tools and skills.\n", claudeplugin.PluginID, dir)
	return nil
}

func cmdClaudeStatus(c *ctx, args []string) error {
	fs := flag.NewFlagSet("claude status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "JSON output")
	if _, _, err := parse(fs, args); err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	installed, err := claudeplugin.Status(cctx)
	if *asJSON {
		out := map[string]any{"installed": installed, "plugin": claudeplugin.PluginID}
		if err != nil {
			out["error"] = err.Error()
		}
		return printJSON(c.stdout, out)
	}
	if err != nil {
		return err
	}
	if installed {
		fmt.Fprintln(c.stdout, "installed")
	} else {
		fmt.Fprintln(c.stdout, "not installed: run `truehand claude install`")
	}
	return nil
}
