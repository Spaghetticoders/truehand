package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spaghetticoders/truehand/internal/agents"
	"github.com/spaghetticoders/truehand/internal/ci"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/gate"
	"github.com/spaghetticoders/truehand/internal/hook"
)

func init() {
	register("hook claude", "Claude Code hook entry point (reads the hook JSON on stdin)", cmdHookClaude)
	register("hook status", "Status hook of sessions started by the Truehand app (prints nothing, always exits 0)", cmdHookStatus)
	register("hook commit-msg", "git commit-msg hook: require conventional commit subjects", cmdHookCommitMsg)
	register("hook pre-push", "git pre-push hook: every pushed commit must be covered by delivered tasks", cmdHookPrePush)
	register("hook install", "Install truehand's git hooks (commit-msg, pre-push) in this repo", cmdHookInstall)
	register("ci verify", "Merge lock: every changed file belongs to a delivered task with passing gates", cmdCIVerify)
}

func cmdHookClaude(c *ctx, args []string) error {
	return hook.Claude(c.stdin, c.stdout, os.Getenv)
}

// cmdHookStatus forwards a hook event to the app server that started this session. The
// status is cosmetic, so every failure is silent and the agent is never held up.
func cmdHookStatus(c *ctx, args []string) error {
	sid, secret, addr, err := agents.ParseSessionEnv(os.Getenv("TRUEHAND_SESSION"))
	if err != nil {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(c.stdin, 256<<10))
	if err != nil {
		return nil
	}
	client := &http.Client{Timeout: 3 * time.Second}
	host := addr
	if addr == agents.SocketAddr {
		sock, err := socketPath()
		if err != nil {
			return nil
		}
		host = "socket"
		client.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}}
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+host+"/hook/status", bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Truehand-Session", sid+":"+secret)
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}
	return nil
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

// cmdHookPrePush verifies what is pushed, not what is checked out: git names the pushed
// commits on stdin, and each one is checked in a temporary worktree.
func cmdHookPrePush(c *ctx, args []string) error {
	ws, err := c.workspace()
	if err != nil {
		return err
	}
	head, _ := ws.Repo.Git("rev-parse", "HEAD")
	head = strings.TrimSpace(head)
	failed := false
	sc := bufio.NewScanner(c.stdin)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 4 || strings.Trim(f[1], "0") == "" || strings.HasPrefix(f[0], "refs/tags/") {
			continue // deletions and tags carry no new work
		}
		ref, sha := f[0], f[1]
		target := ws
		if sha != head {
			dir, err := os.MkdirTemp("", "truehand-push-")
			if err != nil {
				return err
			}
			if _, err := ws.Repo.Git("worktree", "add", "--detach", "--quiet", dir, sha); err != nil {
				os.RemoveAll(dir)
				return fmt.Errorf("checking out %s for verification: %w", ref, err)
			}
			defer func() {
				_, _ = ws.Repo.Git("worktree", "remove", "--force", dir)
				os.RemoveAll(dir)
			}()
			if target, err = core.Open(dir); err != nil {
				return err
			}
		}
		r, err := ci.Verify(target, "")
		if err != nil {
			return fmt.Errorf("%s: %w", ref, err)
		}
		fmt.Fprintf(c.stdout, "%s:\n", ref)
		printCI(c, r)
		failed = failed || !r.Pass
	}
	if failed {
		return exitError{3}
	}
	return nil
}
