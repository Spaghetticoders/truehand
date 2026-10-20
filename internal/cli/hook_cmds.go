package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/spaghetticoders/truehand/internal/ci"
	"github.com/spaghetticoders/truehand/internal/gate"
	"github.com/spaghetticoders/truehand/internal/hook"
)

func init() {
	register("hook claude", "Claude Code hook entry point (reads the hook JSON on stdin)", cmdHookClaude)
	register("hook commit-msg", "git commit-msg hook: require conventional commit subjects", cmdHookCommitMsg)
	register("hook install", "Install truehand's git hooks (commit-msg, pre-push) in this repo", cmdHookInstall)
	register("ci verify", "Merge lock: every changed file belongs to a delivered task with passing gates", cmdCIVerify)
}

func cmdHookClaude(c *ctx, args []string) error {
	return hook.Claude(c.stdin, c.stdout, os.Getenv)
}

func cmdHookCommitMsg(c *ctx, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: truehand hook commit-msg <message-file>")
	}
	b, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	subject := ""
	for _, line := range strings.Split(string(b), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			subject = line
			break
		}
	}
	if strings.HasPrefix(subject, "Merge ") || strings.HasPrefix(subject, "fixup! ") || strings.HasPrefix(subject, "squash! ") {
		return nil
	}
	if !gate.ConventionalSubject(subject) {
		return fmt.Errorf("commit subject %q is not a conventional commit (type(scope): summary, e.g. \"fix(auth): refresh expired token\")", subject)
	}
	return nil
}

func cmdHookInstall(c *ctx, args []string) error {
	fs := flag.NewFlagSet("hook install", flag.ContinueOnError)
	force := fs.Bool("force", false, "replace existing hooks not installed by truehand")
	if _, _, err := parse(fs, args); err != nil {
		return err
	}
	ws, err := c.workspace()
	if err != nil {
		return err
	}
	paths, err := hook.InstallGitHooks(ws, *force)
	for _, p := range paths {
		fmt.Fprintf(c.stdout, "installed %s\n", p)
	}
	return err
}

func cmdCIVerify(c *ctx, args []string) error {
	fs := flag.NewFlagSet("ci verify", flag.ContinueOnError)
	base := fs.String("base", "", "base branch (default: base from config)")
	fs.Bool("changed", true, "verify the changes of this branch (default)")
	asJSON := fs.Bool("json", false, "JSON output")
	if _, _, err := parse(fs, args); err != nil {
		return err
	}
	ws, err := c.workspace()
	if err != nil {
		return err
	}
	r, err := ci.Verify(ws, *base)
	if err != nil {
		return err
	}
	if *asJSON {
		if err := printJSON(c.stdout, r); err != nil {
			return err
		}
	} else {
		printCI(c, r)
	}
	if !r.Pass {
		return exitError{3}
	}
	return nil
}

func printCI(c *ctx, r *ci.Report) {
	w := c.stdout
	fmt.Fprintf(w, "truehand ci verify against %s (gates from %s): %d changed file(s), %d task(s)\n", r.Base, r.GatesFrom, len(r.Changed), len(r.Tasks))
	for _, t := range r.Tasks {
		mark := "✓"
		if !t.Pass {
			mark = "✗"
		}
		fmt.Fprintf(w, "\n%s %s (%s), covers %d file(s)\n", mark, t.ID, t.State, len(t.Covers))
		for _, g := range t.Gates {
			for _, it := range g.Items {
				if it.Status == gate.StatusFail {
					fmt.Fprintf(w, "    ✗ %s/%s: %s\n", g.Target, it.Check, it.Message)
				}
			}
		}
	}
	if len(r.Uncovered) > 0 {
		fmt.Fprintln(w, "\nNot covered by any delivered task:")
		for _, p := range r.Uncovered {
			fmt.Fprintf(w, "  - %s\n", p)
		}
	}
	if r.Pass {
		fmt.Fprintln(w, "\nPASS")
	} else {
		fmt.Fprintf(w, "\nFAIL: %s\n", strings.Join(r.Problems, "; "))
	}
}
