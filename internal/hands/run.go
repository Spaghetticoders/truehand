package hands

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/evidence"
)

// Reserved hand names are produced only by their own hands.
var Reserved = map[string]bool{"test-proof": true, "eyes": true, "review": true}

// Run executes a command and records its log and exit code. Truehand runs the command
// itself, so the evidence cannot claim a pass the command did not produce.
func Run(ctx context.Context, ws *core.Workspace, t *board.Task, name, cmdline string, timeout time.Duration, tee io.Writer) (*Result, error) {
	if Reserved[name] {
		return nil, fmt.Errorf("%q is a built-in hand: use `%s`", name, "truehand hand "+name)
	}
	configured := ws.Config.Command(name)
	if configured != "" && cmdline != "" && cmdline != configured {
		return nil, fmt.Errorf("commands.%s is configured as %q, so `hand run %s` runs exactly that; use another hand name for ad-hoc commands", name, configured, name)
	}
	if cmdline == "" {
		cmdline = configured
	}
	if cmdline == "" {
		return nil, fmt.Errorf("no command for %q: pass one after `--` or set commands.%s in .truehand/config.yaml", name, name)
	}
	s, err := Begin(ws, t, name, "")
	if err != nil {
		return nil, err
	}
	res, err := RunShell(ctx, ws.Repo.Root, cmdline, timeout, tee)
	if err != nil {
		return nil, err
	}
	header := fmt.Sprintf("$ %s\n# exit %d in %s\n\n", cmdline, res.ExitCode, res.Duration.Round(time.Millisecond))
	if err := s.WriteFile("output.log", append([]byte(header), res.Output...)); err != nil {
		return nil, err
	}
	s.Rec.Data = map[string]any{"command": cmdline, "exit_code": res.ExitCode, "duration_ms": res.Duration.Milliseconds()}
	switch {
	case res.TimedOut:
		return s.Finish(evidence.Error, fmt.Sprintf("%s timed out after %s", name, timeout), "")
	case res.ExitCode == 0:
		return s.Finish(evidence.Pass, fmt.Sprintf("`%s` exited 0", cmdline), "")
	default:
		return s.Finish(evidence.Fail, fmt.Sprintf("`%s` exited %d:\n%s", cmdline, res.ExitCode, tail(res.Output, 15)), "fix the failures and run the hand again")
	}
}
