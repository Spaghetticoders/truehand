package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/config"
	"github.com/spaghetticoders/truehand/internal/gitx"

	"github.com/spaghetticoders/truehand/internal/core"
)

// ClaudeInput is the subset of Claude Code's hook payload Truehand reads.
type ClaudeInput struct {
	SessionID      string          `json:"session_id"`
	Cwd            string          `json:"cwd"`
	HookEventName  string          `json:"hook_event_name"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	StopHookActive bool            `json:"stop_hook_active"`
	AgentID        string          `json:"agent_id"`
	Source         string          `json:"source"`
}

// Claude handles one Claude Code hook invocation. Repos without Truehand are left alone, and
// internal errors never block the agent: a broken hook must not brick a session.
func Claude(stdin io.Reader, stdout io.Writer, env func(string) string) error {
	var in ClaudeInput
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		return nil
	}
	dir := in.Cwd
	if dir == "" {
		dir = env("CLAUDE_PROJECT_DIR")
	}
	ws, err := core.Open(dir)
	if err != nil {
		// A repo that has a Truehand config which does not load stays locked: a typo must
		// not switch every lock off. Repos without Truehand are left alone.
		repo, rerr := gitx.Open(dir)
		if rerr != nil {
			return nil
		}
		if _, serr := os.Stat(config.Path(repo.Root)); serr != nil {
			return nil
		}
		if in.HookEventName == "Stop" && !in.StopHookActive {
			return json.NewEncoder(stdout).Encode(map[string]any{"decision": "block", "reason": fmt.Sprintf(
				"Truehand: the config does not load (%v). Commits and pushes are blocked. Tell the user to fix .truehand/config.yaml before you finish.", err)})
		}
		if in.HookEventName != "PreToolUse" {
			return nil
		}
		if d := brokenConfig(repo.Root, in, err); d.Deny {
			return writeDeny(stdout, d)
		}
		return nil
	}
	switch in.HookEventName {
	case "SessionStart":
		return sessionStart(ws, in, stdout, env)
	case "PreToolUse":
		d := preToolUse(ws, in)
		if !d.Deny {
			recordEdit(ws, in)
			return nil
		}
		return writeDeny(stdout, d)
	case "Stop":
		d := CheckStop(ws, in.StopHookActive)
		if !d.Deny {
			return nil
		}
		return json.NewEncoder(stdout).Encode(map[string]any{"decision": "block", "reason": d.Reason})
	}
	return nil
}

func writeDeny(stdout io.Writer, d Decision) error {
	return json.NewEncoder(stdout).Encode(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": d.Reason,
		},
	})
}

// brokenConfig denies what the locks would normally guard while the config does not load.
func brokenConfig(root string, in ClaudeInput, cfgErr error) Decision {
	var ti struct {
		Command  string `json:"command"`
		FilePath string `json:"file_path"`
	}
	_ = json.Unmarshal(in.ToolInput, &ti)
	why := fmt.Sprintf("the Truehand config does not load (%v), so commits, pushes and edits under .truehand are blocked until the user fixes .truehand/config.yaml", cfgErr)
	switch in.ToolName {
	case "Bash":
		for _, seg := range segments(ti.Command) {
			if sub, _, _ := gitCommand(stripEnv(seg)); sub == "commit" || sub == "push" {
				return deny("%s", why)
			}
		}
		if strings.Contains(ti.Command, ".truehand") {
			return deny("%s", why)
		}
	case "Write", "Edit", "MultiEdit", "NotebookEdit":
		p := abs(in.Cwd, ti.FilePath)
		if rel, err := filepath.Rel(realPath(root), realPath(p)); err == nil && (rel == ".truehand" || strings.HasPrefix(rel, ".truehand"+string(filepath.Separator))) {
			return deny("%s", why)
		}
	}
	return allow()
}

// recordEdit notes who edits files during an active task, so the review gate knows that a
// session which only wrote code (and ran no hand) also worked on the task.
func recordEdit(ws *core.Workspace, in ClaudeInput) {
	switch in.ToolName {
	case "Write", "Edit", "MultiEdit", "NotebookEdit":
	default:
		return
	}
	id := ws.ActiveTask()
	if id == "" || in.SessionID == "" {
		return
	}
	t, err := ws.Store.Load(id)
	if err != nil || t.State != board.Building {
		return
	}
	actor := "claude:" + in.SessionID
	events, _ := ws.Store.Events(id)
	for _, e := range events {
		if e.Type == "edited" && e.Actor == actor {
			return
		}
	}
	_ = ws.Store.AppendEvent(id, board.Event{Type: "edited", Actor: actor})
}

func preToolUse(ws *core.Workspace, in ClaudeInput) Decision {
	var ti struct {
		Command      string `json:"command"`
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
	}
	_ = json.Unmarshal(in.ToolInput, &ti)
	switch in.ToolName {
	case "Bash":
		return CheckBash(ws, in.Cwd, ti.Command)
	case "Write", "Edit", "MultiEdit":
		return CheckWrite(ws, abs(in.Cwd, ti.FilePath))
	case "NotebookEdit":
		return CheckWrite(ws, abs(in.Cwd, ti.NotebookPath))
	}
	return allow()
}

// sessionStart gives the session an actor identity for its Bash commands and tells the agent
// where the active task stands.
func sessionStart(ws *core.Workspace, in ClaudeInput, stdout io.Writer, env func(string) string) error {
	ws.RecordSession(in.SessionID)
	if f := env("CLAUDE_ENV_FILE"); f != "" && in.SessionID != "" {
		fh, err := os.OpenFile(f, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if err == nil {
			fmt.Fprintf(fh, "export TRUEHAND_ACTOR=%s\n", shellQuote("claude:"+in.SessionID))
			fh.Close()
		}
	}
	return json.NewEncoder(stdout).Encode(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": Context(ws),
		},
	})
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
