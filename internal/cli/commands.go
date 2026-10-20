package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/config"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/evidence"
	"github.com/spaghetticoders/truehand/internal/gate"
	"github.com/spaghetticoders/truehand/internal/gitx"
	"github.com/spaghetticoders/truehand/internal/hands"
	"github.com/spaghetticoders/truehand/internal/humanauth"
	"github.com/spaghetticoders/truehand/internal/plan"
)

// isTTY reports whether stdin is an interactive terminal. Tests replace it.
var isTTY = func() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func init() {
	register("init", "Create .truehand/config.yaml for this repo", cmdInit)
	register("task new", "Create a task", cmdTaskNew)
	register("task list", "List tasks and their state", cmdTaskList)
	register("task show", "Show a task, its gate for the next state and its evidence", cmdTaskShow)
	register("board", "Print the whole board of this repo (for apps; use --json)", cmdBoard)
	register("task use", "Set the active task for this clone", cmdTaskUse)
	register("task move", "Move a task to a state (forward moves must pass the gate)", cmdTaskMove)
	register("gate check", "Evaluate a gate without moving the task", cmdGateCheck)
	register("plan init", "Write the plan.md template for a task", cmdPlanInit)
	register("plan check", "Validate the structure of a task's plan.md", cmdPlanCheck)
	register("approve", "Approve a task's plan (humans only: needs a terminal)", cmdApprove)
	register("hand run", "Run a command as a hand and record its output as evidence", cmdHandRun)
	register("review", "Record a review of the task (by someone other than the developer)", cmdReview)
	register("evidence list", "List a task's evidence and whether it is still fresh", cmdEvidenceList)
}

func (c *ctx) workspace() (*core.Workspace, error) { return core.Open(c.dir) }

func cmdInit(c *ctx, args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	stack := fs.String("stack", "", "stack preset: "+strings.Join(config.Stacks, ", ")+" (default: detected)")
	force := fs.Bool("force", false, "overwrite an existing config")
	if _, _, err := parse(fs, args); err != nil {
		return err
	}
	repo, err := gitx.Open(c.dir)
	if err != nil {
		return err
	}
	if *stack == "" {
		*stack = detectStack(repo.Root)
	}
	tpl, err := config.Template(*stack, detectBase(repo))
	if err != nil {
		return err
	}
	p := config.Path(repo.Root)
	if _, err := os.Stat(p); err == nil && !*force {
		return fmt.Errorf("%s already exists (use --force to overwrite)", p)
	}
	if err := os.MkdirAll(filepath.Join(repo.Root, config.Dir, "tasks"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(tpl), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "Created %s (stack: %s)\nNext: truehand task new <id> --title \"...\"\n", filepath.Join(config.Dir, config.File), *stack)
	return nil
}

// detectBase picks the branch tasks are measured against: the remote default branch when
// there is one, else the current local branch.
func detectBase(repo *gitx.Repo) string {
	if out, err := repo.Git("symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		return strings.TrimSpace(out)
	}
	for _, ref := range []string{"origin/main", "origin/master"} {
		if _, err := repo.Git("rev-parse", "--verify", "-q", ref); err == nil {
			return ref
		}
	}
	if out, err := repo.Git("branch", "--show-current"); err == nil && strings.TrimSpace(out) != "" {
		return strings.TrimSpace(out)
	}
	return "main"
}

func detectStack(root string) string {
	for file, stack := range map[string]string{"pubspec.yaml": "flutter", "go.mod": "go", "package.json": "node"} {
		if _, err := os.Stat(filepath.Join(root, file)); err == nil {
			return stack
		}
	}
	return "generic"
}

func cmdTaskNew(c *ctx, args []string) error {
	fs := flag.NewFlagSet("task new", flag.ContinueOnError)
	title := fs.String("title", "", "task title (required)")
	source := fs.String("source", "", "ticket reference, e.g. linear:SPA-123")
	scope := fs.String("scope", "", "comma-separated globs of the files this task owns (needed when tasks share a branch)")
	use := fs.Bool("use", true, "make it the active task")
	asJSON := fs.Bool("json", false, "JSON output")
	pos, _, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *title == "" {
		return errors.New("usage: truehand task new <id> --title \"...\" [--source linear:SPA-1]")
	}
	ws, err := c.workspace()
	if err != nil {
		return err
	}
	t, err := ws.NewTask(pos[0], *title, *source, splitGlobs(*scope)...)
	if err != nil {
		return err
	}
	if *use {
		if err := ws.SetActiveTask(t.ID); err != nil {
			return err
		}
	}
	if *asJSON {
		return printJSON(c.stdout, t)
	}
	fmt.Fprintf(c.stdout, "Created %s (%s), base %s\nNext: truehand plan init %s\n", t.ID, t.State, shortSHA(t.Base), t.ID)
	return nil
}

func cmdTaskList(c *ctx, args []string) error {
	fs := flag.NewFlagSet("task list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "JSON output")
	if _, _, err := parse(fs, args); err != nil {
		return err
	}
	ws, err := c.workspace()
	if err != nil {
		return err
	}
	tasks, err := ws.Store.List()
	if err != nil {
		return err
	}
	if *asJSON {
		if tasks == nil {
			tasks = []*board.Task{}
		}
		return printJSON(c.stdout, tasks)
	}
	if len(tasks) == 0 {
		fmt.Fprintln(c.stdout, "No tasks. Create one with: truehand task new <id> --title \"...\"")
		return nil
	}
	active := ws.ActiveTask()
	for _, t := range tasks {
		mark := " "
		if t.ID == active {
			mark = "*"
		}
		state := string(t.State)
		if ws.Stale(t) {
			state += " (stale)"
		}
		fmt.Fprintf(c.stdout, "%s %-14s %-18s %s\n", mark, t.ID, state, t.Title)
	}
	return nil
}

func cmdTaskShow(c *ctx, args []string) error {
	fs := flag.NewFlagSet("task show", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "JSON output")
	full := fs.Bool("full", false, "with --json: include the plan and the event log")
	pos, _, err := parse(fs, args)
	if err != nil {
		return err
	}
	ws, err := c.workspace()
	if err != nil {
		return err
	}
	t, err := ws.ResolveTask(first(pos))
	if err != nil {
		return err
	}
	if *asJSON && *full {
		d, err := ws.Detail(t)
		if err != nil {
			return err
		}
		return printJSON(c.stdout, d)
	}
	v, err := ws.View(t)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(c.stdout, v)
	}
	fmt.Fprintf(c.stdout, "%s — %s\n  state:  %s\n  base:   %s\n", t.ID, t.Title, t.State, shortSHA(t.Base))
	if t.Source != "" {
		fmt.Fprintf(c.stdout, "  source: %s\n", t.Source)
	}
	if len(t.Scope) > 0 {
		fmt.Fprintf(c.stdout, "  scope:  %s\n", strings.Join(t.Scope, ", "))
	}
	for role, who := range t.Roles {
		fmt.Fprintf(c.stdout, "  %s: %s\n", role, who)
	}
	if v.NextGate != nil {
		fmt.Fprintln(c.stdout)
		printGate(c.stdout, *v.NextGate)
	}
	if len(v.Evidence) > 0 {
		fmt.Fprintln(c.stdout, "\nEvidence (latest per hand):")
		printEvidence(c.stdout, v.Evidence)
	}
	return nil
}

func cmdBoard(c *ctx, args []string) error {
	fs := flag.NewFlagSet("board", flag.ContinueOnError)
	fs.Bool("json", true, "JSON output (the only format)")
	if _, _, err := parse(fs, args); err != nil {
		return err
	}
	ws, err := c.workspace()
	if err != nil {
		return err
	}
	b, err := ws.Board()
	if err != nil {
		return err
	}
	return printJSON(c.stdout, b)
}

func cmdTaskUse(c *ctx, args []string) error {
	fs := flag.NewFlagSet("task use", flag.ContinueOnError)
	clear := fs.Bool("clear", false, "clear the active task")
	pos, _, err := parse(fs, args)
	if err != nil {
		return err
	}
	ws, err := c.workspace()
	if err != nil {
		return err
	}
	if *clear {
		return ws.SetActiveTask("")
	}
	if len(pos) != 1 {
		if a := ws.ActiveTask(); a != "" {
			fmt.Fprintln(c.stdout, a)
			return nil
		}
		return errors.New("no active task")
	}
	if _, err := ws.Store.Load(pos[0]); err != nil {
		return err
	}
	return ws.SetActiveTask(pos[0])
}

// taskAndState accepts "<state>" (active task) or "<id> <state>".
func taskAndState(ws *core.Workspace, pos []string) (*board.Task, board.State, error) {
	var id, st string
	switch len(pos) {
	case 1:
		st = pos[0]
	case 2:
		id, st = pos[0], pos[1]
	default:
		return nil, "", errors.New("expected [task-id] <state>")
	}
	state, err := board.ParseState(st)
	if err != nil {
		return nil, "", err
	}
	t, err := ws.ResolveTask(id)
	return t, state, err
}

func cmdTaskMove(c *ctx, args []string) error {
	fs := flag.NewFlagSet("task move", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "JSON output")
	pos, _, err := parse(fs, args)
	if err != nil {
		return err
	}
	ws, err := c.workspace()
	if err != nil {
		return err
	}
	t, target, err := taskAndState(ws, pos)
	if err != nil {
		return err
	}
	from := t.State
	res, err := ws.Move(t, target)
	var me *core.MoveError
	if errors.As(err, &me) {
		if *asJSON {
			_ = printJSON(c.stdout, res)
		} else {
			printGate(c.stdout, res)
		}
		return exitError{3}
	}
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(c.stdout, res)
	}
	if len(res.Items) > 0 {
		printGate(c.stdout, res)
	}
	fmt.Fprintf(c.stdout, "%s: %s → %s\n", t.ID, from, target)
	return nil
}

func cmdGateCheck(c *ctx, args []string) error {
	fs := flag.NewFlagSet("gate check", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "JSON output")
	pos, _, err := parse(fs, args)
	if err != nil {
		return err
	}
	ws, err := c.workspace()
	if err != nil {
		return err
	}
	var t *board.Task
	var target board.State
	if len(pos) == 0 {
		if t, err = ws.ResolveTask(""); err != nil {
			return err
		}
		if target = t.State.Next(); target == "" {
			return fmt.Errorf("%s is %s: no next gate", t.ID, t.State)
		}
	} else if t, target, err = taskAndState(ws, pos); err != nil {
		return err
	}
	res, err := ws.Check(t, target)
	if err != nil {
		return err
	}
	if *asJSON {
		err = printJSON(c.stdout, res)
	} else {
		printGate(c.stdout, res)
	}
	if err == nil && !res.Pass {
		return exitError{3}
	}
	return err
}

func cmdPlanInit(c *ctx, args []string) error {
	fs := flag.NewFlagSet("plan init", flag.ContinueOnError)
	pos, _, err := parse(fs, args)
	if err != nil {
		return err
	}
	ws, err := c.workspace()
	if err != nil {
		return err
	}
	t, err := ws.ResolveTask(first(pos))
	if err != nil {
		return err
	}
	p := ws.Store.PlanPath(t.ID)
	if _, err := os.Stat(p); err == nil {
		fmt.Fprintf(c.stdout, "%s already exists\n", ws.Store.Rel(p))
		return nil
	}
	ctxNote := ""
	if _, err := os.Stat(filepath.Join(ws.Store.TaskDir(t.ID), "context.md")); err == nil {
		ctxNote = "context.md"
	}
	if err := os.WriteFile(p, []byte(plan.Template(t.ID, t.Title, ctxNote)), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "Wrote %s — fill every section, then: truehand plan check %s\n", ws.Store.Rel(p), t.ID)
	return nil
}

func cmdPlanCheck(c *ctx, args []string) error {
	fs := flag.NewFlagSet("plan check", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "JSON output")
	pos, _, err := parse(fs, args)
	if err != nil {
		return err
	}
	ws, err := c.workspace()
	if err != nil {
		return err
	}
	t, err := ws.ResolveTask(first(pos))
	if err != nil {
		return err
	}
	b, err := os.ReadFile(ws.Store.PlanPath(t.ID))
	if err != nil {
		return fmt.Errorf("no plan.md: run `truehand plan init %s`", t.ID)
	}
	probs := plan.Check(string(b))
	if *asJSON {
		if probs == nil {
			probs = []plan.Problem{}
		}
		if err := printJSON(c.stdout, map[string]any{"valid": len(probs) == 0, "problems": probs}); err != nil {
			return err
		}
	} else if len(probs) == 0 {
		fmt.Fprintf(c.stdout, "✓ plan.md for %s is complete\n", t.ID)
	} else {
		fmt.Fprintf(c.stdout, "✗ plan.md for %s is incomplete:\n", t.ID)
		for _, p := range probs {
			fmt.Fprintf(c.stdout, "  - %s: %s\n", p.Section, p.Message)
		}
	}
	if len(probs) > 0 {
		return exitError{3}
	}
	return nil
}

func cmdApprove(c *ctx, args []string) error {
	fs := flag.NewFlagSet("approve", flag.ContinueOnError)
	planSHA := fs.String("plan-sha", "", "approve from an app that showed the plan: the sha256 of the plan the user read (skips the terminal confirmation, never the OS authentication)")
	pos, _, err := parse(fs, args)
	if err != nil {
		return err
	}
	ws, err := c.workspace()
	if err != nil {
		return err
	}
	t, err := ws.ResolveTask(first(pos))
	if err != nil {
		return err
	}
	if ws.InAgentSession() || os.Getenv("TRUEHAND_ACTOR") != "" {
		return errors.New("approval runs in your own terminal, not inside an agent session (or with TRUEHAND_ACTOR set). Agents cannot approve")
	}
	if *planSHA == "" && !isTTY() {
		return errors.New("approval needs a human at a terminal (stdin is not a TTY). Agents cannot approve")
	}
	b, err := os.ReadFile(ws.Store.PlanPath(t.ID))
	if err != nil {
		return fmt.Errorf("no plan.md to approve for %s", t.ID)
	}
	if probs := plan.Check(string(b)); len(probs) > 0 {
		return fmt.Errorf("plan.md is incomplete (%d problem(s)): run `truehand plan check %s`", len(probs), t.ID)
	}
	via := humanauth.Method()
	if *planSHA != "" {
		if *planSHA != evidence.HashBytes(b) {
			return errors.New("plan.md changed since it was shown: reload it and read it again")
		}
		via = "app+" + via
	} else {
		fmt.Fprintf(c.stdout, "%s\n\nApprove this plan for %s? Type the task id to confirm: ", strings.TrimSpace(string(b)), t.ID)
		line, _ := bufio.NewReader(c.stdin).ReadString('\n')
		if strings.TrimSpace(line) != t.ID {
			return errors.New("not approved")
		}
	}
	// The process-tree and TTY checks stop honest mistakes; the OS dialog is what an agent
	// cannot pass, since it needs the user's password or fingerprint.
	if err := humanauth.Authenticate(humanauth.ApprovalPrompt(t.ID, t.Title)); err != nil {
		return fmt.Errorf("not approved: %w", err)
	}
	a := board.Approval{
		State: board.Approved, Name: ws.Repo.ConfigValue("user.name"), Email: ws.Repo.ConfigValue("user.email"),
		PlanSHA: evidence.HashBytes(b), Via: via,
	}
	if err := ws.Store.AppendApproval(t.ID, a); err != nil {
		return err
	}
	_ = ws.Store.AppendEvent(t.ID, board.Event{Type: "approved", Actor: "human:" + a.Email})
	fmt.Fprintf(c.stdout, "Approved. Next: truehand task move %s approved\n", t.ID)
	return nil
}

func cmdHandRun(c *ctx, args []string) error {
	fs := flag.NewFlagSet("hand run", flag.ContinueOnError)
	task := fs.String("task", "", "task id (default: active task)")
	timeout := fs.Duration("timeout", 30*time.Minute, "kill the command after this long")
	asJSON := fs.Bool("json", false, "JSON output")
	quiet := fs.Bool("quiet", false, "do not stream the command output")
	pos, tail, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: truehand hand run <name> [--task id] [-- command ...]")
	}
	ws, err := c.workspace()
	if err != nil {
		return err
	}
	t, err := ws.ResolveTask(*task)
	if err != nil {
		return err
	}
	var tee io.Writer
	if !*quiet && !*asJSON {
		tee = c.stderr
	}
	res, err := hands.Run(context.Background(), ws, t, pos[0], strings.Join(tail, " "), *timeout, tee)
	if err != nil {
		return err
	}
	return printHand(c, res, *asJSON)
}

func cmdReview(c *ctx, args []string) error {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	task := fs.String("task", "", "task id (default: active task)")
	verdict := fs.String("verdict", "", "pass or fail (required)")
	notes := fs.String("notes", "", "markdown file with the review notes (required)")
	asJSON := fs.Bool("json", false, "JSON output")
	if _, _, err := parse(fs, args); err != nil {
		return err
	}
	if (*verdict != evidence.Pass && *verdict != evidence.Fail) || *notes == "" {
		return errors.New("usage: truehand review --verdict pass|fail --notes review.md [--task id]")
	}
	ws, err := c.workspace()
	if err != nil {
		return err
	}
	t, err := ws.ResolveTask(*task)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(*notes)
	if err != nil {
		return err
	}
	res, err := hands.Review(ws, t, *verdict, b)
	if err != nil {
		return err
	}
	return printHand(c, res, *asJSON)
}

func cmdEvidenceList(c *ctx, args []string) error {
	fs := flag.NewFlagSet("evidence list", flag.ContinueOnError)
	all := fs.Bool("all", false, "include superseded records")
	asJSON := fs.Bool("json", false, "JSON output")
	pos, _, err := parse(fs, args)
	if err != nil {
		return err
	}
	ws, err := c.workspace()
	if err != nil {
		return err
	}
	t, err := ws.ResolveTask(first(pos))
	if err != nil {
		return err
	}
	views, err := ws.Evidence(t, *all)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(c.stdout, views)
	}
	if len(views) == 0 {
		fmt.Fprintf(c.stdout, "No evidence for %s yet.\n", t.ID)
		return nil
	}
	printEvidence(c.stdout, views)
	return nil
}

func printHand(c *ctx, res *hands.Result, asJSON bool) error {
	if asJSON {
		if err := printJSON(c.stdout, res); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(c.stdout, "%s %s [%s]: %s\n", verdictMark(res.Verdict), res.Record, res.Verdict, res.Summary)
		for _, f := range res.Evidence {
			fmt.Fprintf(c.stdout, "  evidence: .truehand/tasks/%s/%s\n", res.Task, f.Path)
		}
		if res.Next != "" {
			fmt.Fprintf(c.stdout, "  next: %s\n", res.Next)
		}
	}
	if res.Verdict != evidence.Pass {
		return exitError{3}
	}
	return nil
}

func printGate(w io.Writer, r gate.Result) {
	state := "OPEN"
	if !r.Pass {
		state = "CLOSED"
	}
	fmt.Fprintf(w, "Gate %s for %s: %s\n", r.Target, r.Task, state)
	if len(r.Items) == 0 {
		fmt.Fprintln(w, "  (no rules)")
	}
	for _, it := range r.Items {
		mark := map[string]string{gate.StatusPass: "✓", gate.StatusFail: "✗", gate.StatusSkip: "–"}[it.Status]
		fmt.Fprintf(w, "  %s %-22s %s\n", mark, it.Check, it.Message)
	}
}

func printEvidence(w io.Writer, views []core.EvidenceView) {
	for _, v := range views {
		fresh := "fresh"
		if !v.Fresh {
			fresh = "STALE"
		}
		if !v.Intact {
			fresh = "TAMPERED"
		}
		fmt.Fprintf(w, "  %s %-16s %-5s %-8s %s  %s\n", verdictMark(v.Verdict), v.ID, v.Verdict, fresh, v.Created.Local().Format("01-02 15:04"), firstLine(v.Summary))
	}
}

func verdictMark(v string) string {
	switch v {
	case evidence.Pass:
		return "✓"
	case evidence.Fail:
		return "✗"
	}
	return "!"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func splitGlobs(s string) []string {
	var out []string
	for _, g := range strings.Split(s, ",") {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, g)
		}
	}
	return out
}

func first(s []string) string {
	if len(s) > 0 {
		return s[0]
	}
	return ""
}

func shortSHA(s string) string {
	if len(s) > 10 {
		return s[:10]
	}
	return s
}
