package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spaghetticoders/truehand/internal/hands"
)

func init() {
	register("hand test-proof", "Prove the tests catch the change: pass with it, fail without it", cmdTestProof)
}

func cmdTestProof(c *ctx, args []string) error {
	fs := flag.NewFlagSet("hand test-proof", flag.ContinueOnError)
	task := fs.String("task", "", "task id (default: active task)")
	command := fs.String("command", "", "test command (default: commands.test_proof, then commands.test)")
	only := fs.String("only", "", "comma-separated globs: revert only these files")
	timeout := fs.Duration("timeout", 30*time.Minute, "timeout of each test run")
	restore := fs.Bool("restore", false, "restore files left reverted by an interrupted run")
	asJSON := fs.Bool("json", false, "JSON output")
	quiet := fs.Bool("quiet", false, "do not stream test output")
	if _, _, err := parse(fs, args); err != nil {
		return err
	}
	ws, err := c.workspace()
	if err != nil {
		return err
	}
	if *restore {
		n, err := hands.Restore(ws)
		if err != nil {
			return err
		}
		fmt.Fprintf(c.stdout, "Restored %d file(s)\n", n)
		return nil
	}
	t, err := ws.ResolveTask(*task)
	if err != nil {
		return err
	}
	var globs []string
	for _, g := range strings.Split(*only, ",") {
		if g = strings.TrimSpace(g); g != "" {
			globs = append(globs, g)
		}
	}
	var tee io.Writer
	if !*quiet && !*asJSON {
		tee = c.stderr
	}
	res, err := hands.TestProof(context.Background(), ws, t, hands.TestProofOptions{Command: *command, Only: globs, Timeout: *timeout, Tee: tee})
	if err != nil {
		return err
	}
	return printHand(c, res, *asJSON)
}

func init() {
	register("hand eyes", "Compare a screen of the running app with its Figma node (pixels and texts)", cmdEyes)
}

func cmdEyes(c *ctx, args []string) error {
	fs := flag.NewFlagSet("hand eyes", flag.ContinueOnError)
	task := fs.String("task", "", "task id (default: active task)")
	screenshot := fs.String("screenshot", "", "compare this PNG instead of capturing the simulator")
	launch := fs.Bool("launch", false, "relaunch the app before capturing")
	all := fs.Bool("all", false, "check every configured screen")
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
	screens := pos
	if *all {
		screens = nil
		for name := range ws.Config.Eyes.Screens {
			screens = append(screens, name)
		}
		sort.Strings(screens)
	}
	if len(screens) == 0 {
		return errors.New("usage: truehand hand eyes <screen>... | --all")
	}
	if *screenshot != "" && len(screens) > 1 {
		return errors.New("--screenshot works with one screen at a time")
	}
	failed := false
	var results []*hands.Result
	for _, sc := range screens {
		res, err := hands.Eyes(context.Background(), ws, t, sc, hands.EyesOptions{Screenshot: *screenshot, Launch: *launch})
		if err != nil {
			return fmt.Errorf("%s: %w", sc, err)
		}
		results = append(results, res)
		if res.Verdict != "pass" {
			failed = true
		}
		if !*asJSON {
			_ = printHand(c, res, false)
		}
	}
	if *asJSON {
		if err := printJSON(c.stdout, results); err != nil {
			return err
		}
	}
	if failed {
		return exitError{3}
	}
	return nil
}
