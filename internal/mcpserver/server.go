// Package mcpserver exposes the board and the hands as MCP tools, so any MCP-capable agent
// uses the same operations as the CLI. There is deliberately no approve tool.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/hands"
	"github.com/spaghetticoders/truehand/internal/plan"
)

// Opener returns the workspace for each call, so config edits are picked up without restart.
type Opener func() (*core.Workspace, error)

type TaskArg struct {
	Task string `json:"task,omitempty" jsonschema:"task id; defaults to the active task"`
}

type newTaskArgs struct {
	ID          string   `json:"id,omitempty" jsonschema:"task id; omit to get the next free one (e.g. T-4)"`
	Title       string   `json:"title" jsonschema:"short title"`
	Description string   `json:"description,omitempty" jsonschema:"what has to be done and why, in markdown: this is the ticket"`
	Source      string   `json:"source,omitempty" jsonschema:"optional external ticket, e.g. linear:SPA-123 or github:owner/repo#12"`
	Scope       []string `json:"scope,omitempty" jsonschema:"globs of the files this task owns; needed when several tasks share a branch"`
}

type commentArgs struct {
	TaskArg
	Body string `json:"body,omitempty" jsonschema:"the comment, in markdown"`
	Text string `json:"text,omitempty" jsonschema:"same as body"`
}

type moveArgs struct {
	TaskArg
	State string `json:"state" jsonschema:"target state: planned, approved, building, verified, reviewed, delivered"`
}

type gateArgs struct {
	TaskArg
	State string `json:"state,omitempty" jsonschema:"state whose gate to check; defaults to the next state"`
}

type fetchArgs struct {
	TaskArg
	Ref string `json:"ref,omitempty" jsonschema:"external ticket reference; defaults to the task source, if any"`
}

type runArgs struct {
	TaskArg
	Name    string `json:"name" jsonschema:"hand name, e.g. analyze or lint; uses commands.<name> from config when command is empty"`
	Command string `json:"command,omitempty" jsonschema:"shell command to run instead of commands.<name>"`
}

type proofArgs struct {
	TaskArg
	Command string   `json:"command,omitempty" jsonschema:"test command; defaults to commands.test_proof or commands.test"`
	Only    []string `json:"only,omitempty" jsonschema:"globs: revert only these files"`
}

type eyesArgs struct {
	TaskArg
	Screen     string `json:"screen" jsonschema:"screen name from eyes.screens"`
	Screenshot string `json:"screenshot,omitempty" jsonschema:"path of a PNG to compare instead of capturing the simulator"`
	Launch     bool   `json:"launch,omitempty" jsonschema:"relaunch the app before capturing"`
}

type reviewArgs struct {
	TaskArg
	Verdict string `json:"verdict" jsonschema:"pass or fail"`
	Notes   string `json:"notes" jsonschema:"the review in markdown: what was read, what was run, findings"`
}

type attachArgs struct {
	TaskArg
	Files []string `json:"files" jsonschema:"paths of the files to keep (screenshots, recordings, reports); relative to the repo root or absolute"`
	Note  string   `json:"note" jsonschema:"what the files show, in markdown"`
}

// New builds the server. version is reported to clients.
func New(open Opener, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "truehand", Version: version}, &mcp.ServerOptions{
		Instructions: "Truehand guards the workflow of this repo. Work happens inside tasks that move through planned → approved → building → verified → reviewed → delivered; each forward move is gated by evidence that only the hands produce. Start with truehand_status. Approvals are for humans only.",
	})

	tool(s, "truehand_status", "Show a task: state, the gate to the next state with what is missing, and its evidence.", func(ctx context.Context, ws *core.Workspace, a TaskArg) (any, error) {
		t, err := ws.ResolveTask(a.Task)
		if err != nil {
			tasks, lerr := ws.Store.List()
			if tasks == nil {
				tasks = []*board.Task{}
			}
			if lerr == nil {
				return map[string]any{"error": err.Error(), "tasks": tasks}, nil
			}
			return nil, err
		}
		return ws.View(t)
	}, open)

	tool(s, "truehand_task_new", "Create a task (state planned) and make it active. The description is the ticket; source links an external one.", func(ctx context.Context, ws *core.Workspace, a newTaskArgs) (any, error) {
		id := a.ID
		if id == "" {
			var err error
			if id, err = ws.Store.NextID(ws.Config.TaskPrefix()); err != nil {
				return nil, err
			}
		}
		t, err := ws.NewTask(id, a.Title, a.Source, a.Scope...)
		if err != nil {
			return nil, err
		}
		if err := ws.Store.SetDescription(t.ID, a.Description); err != nil {
			return nil, err
		}
		return t, ws.SetActiveTask(t.ID)
	}, open)

	tool(s, "truehand_task_comment", "Add a comment to the task: open questions for the human, decisions, findings.", func(ctx context.Context, ws *core.Workspace, a commentArgs) (any, error) {
		t, err := ws.ResolveTask(a.Task)
		if err != nil {
			return nil, err
		}
		body := a.Body
		if body == "" {
			body = a.Text
		}
		return map[string]any{"commented": true}, ws.Comment(t.ID, body)
	}, open)

	tool(s, "truehand_task_move", "Move a task to a state. Forward moves go one step at a time and only if the gate passes; the result lists every check.", func(ctx context.Context, ws *core.Workspace, a moveArgs) (any, error) {
		t, err := ws.ResolveTask(a.Task)
		if err != nil {
			return nil, err
		}
		st, err := board.ParseState(a.State)
		if err != nil {
			return nil, err
		}
		res, err := ws.Move(t, st)
		var me *core.MoveError
		if errors.As(err, &me) {
			return map[string]any{"moved": false, "gate": res}, nil
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"moved": true, "state": t.State, "gate": res}, nil
	}, open)

	tool(s, "truehand_gate_check", "Evaluate a gate without moving the task.", func(ctx context.Context, ws *core.Workspace, a gateArgs) (any, error) {
		t, err := ws.ResolveTask(a.Task)
		if err != nil {
			return nil, err
		}
		target := t.State.Next()
		if a.State != "" {
			if target, err = board.ParseState(a.State); err != nil {
				return nil, err
			}
		}
		if target == "" {
			return nil, fmt.Errorf("%s is %s: no next gate", t.ID, t.State)
		}
		return ws.Check(t, target)
	}, open)

	tool(s, "truehand_plan_fetch", "Write context.md (the task description and comments, plus the linked external ticket if any) and create the plan.md template.", func(ctx context.Context, ws *core.Workspace, a fetchArgs) (any, error) {
		t, err := ws.ResolveTask(a.Task)
		if err != nil {
			return nil, err
		}
		return hands.FetchPlanContext(ctx, ws, t, a.Ref)
	}, open)

	tool(s, "truehand_plan_check", "Validate the structure of the task's plan.md (sections, a verify: line per step).", func(ctx context.Context, ws *core.Workspace, a TaskArg) (any, error) {
		t, err := ws.ResolveTask(a.Task)
		if err != nil {
			return nil, err
		}
		b, err := os.ReadFile(ws.Store.PlanPath(t.ID))
		if err != nil {
			return nil, fmt.Errorf("no plan.md for %s: call truehand_plan_fetch or write it", t.ID)
		}
		probs := plan.Check(string(b))
		if probs == nil {
			probs = []plan.Problem{}
		}
		return map[string]any{"valid": len(probs) == 0, "problems": probs, "path": ws.Store.Rel(ws.Store.PlanPath(t.ID))}, nil
	}, open)

	tool(s, "truehand_hand_run", "Run a command as a hand (truehand runs it) and record its output and exit code as evidence.", func(ctx context.Context, ws *core.Workspace, a runArgs) (any, error) {
		t, err := ws.ResolveTask(a.Task)
		if err != nil {
			return nil, err
		}
		return hands.Run(ctx, ws, t, a.Name, a.Command, 30*time.Minute, nil)
	}, open)

	tool(s, "truehand_test_proof", "Prove the tests catch the change: they must pass with it and fail with the non-test files reverted to the task base. Files are always restored.", func(ctx context.Context, ws *core.Workspace, a proofArgs) (any, error) {
		t, err := ws.ResolveTask(a.Task)
		if err != nil {
			return nil, err
		}
		return hands.TestProof(ctx, ws, t, hands.TestProofOptions{Command: a.Command, Only: a.Only, Timeout: 30 * time.Minute})
	}, open)

	tool(s, "truehand_eyes", "Capture a screen of the running app and compare it with its Figma node: pixel diff and every visible Figma text on screen. Evidence: actual.png, design.png, diff.png, report.json.", func(ctx context.Context, ws *core.Workspace, a eyesArgs) (any, error) {
		t, err := ws.ResolveTask(a.Task)
		if err != nil {
			return nil, err
		}
		return hands.Eyes(ctx, ws, t, a.Screen, hands.EyesOptions{Screenshot: a.Screenshot, Launch: a.Launch})
	}, open)

	tool(s, "truehand_review", "Record a review of the task. The reviewer must not be the developer of the task.", func(ctx context.Context, ws *core.Workspace, a reviewArgs) (any, error) {
		t, err := ws.ResolveTask(a.Task)
		if err != nil {
			return nil, err
		}
		return hands.Review(ws, t, a.Verdict, []byte(a.Notes))
	}, open)

	tool(s, "truehand_attach", "Keep screenshots or other files on the task as evidence for the humans (they see them on the board), with a note on what they show.", func(ctx context.Context, ws *core.Workspace, a attachArgs) (any, error) {
		t, err := ws.ResolveTask(a.Task)
		if err != nil {
			return nil, err
		}
		return hands.Attach(ws, t, a.Note, a.Files)
	}, open)

	return s
}

// tool registers a handler that opens the workspace and returns its result as JSON text.
// Errors become tool errors the agent can read, not protocol failures.
func tool[In any](s *mcp.Server, name, desc string, fn func(context.Context, *core.Workspace, In) (any, error), open Opener) {
	mcp.AddTool(s, &mcp.Tool{Name: name, Description: desc}, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		ws, err := open()
		if err != nil {
			return errResult(err), nil, nil
		}
		out, err := fn(ctx, ws, in)
		if err != nil {
			return errResult(err), nil, nil
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			return errResult(err), nil, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: buf.String()}}}, nil, nil
	})
}

func errResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}
}
