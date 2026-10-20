package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/spaghetticoders/truehand/internal/core"
)

func session(t *testing.T) (*mcp.ClientSession, string) {
	dir := t.TempDir()
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "a@b.c"}, {"commit", "-q", "--allow-empty", "-m", "chore: x"}} {
		cmd := exec.Command("git", a...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(string(out))
		}
	}
	os.MkdirAll(filepath.Join(dir, ".truehand"), 0o755)
	os.WriteFile(filepath.Join(dir, ".truehand/config.yaml"), []byte("version: 0\nbase: main\ncommands:\n  check: \"test -f ok.txt\"\ngates:\n  approved:\n    - check: human_approval\n"), 0o644)
	srv := New(func() (*core.Workspace, error) { return core.Open(dir) }, "test")
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs, dir
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

func TestTools(t *testing.T) {
	cs, dir := session(t)
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tl := range tools.Tools {
		names[tl.Name] = true
	}
	for _, want := range []string{"truehand_status", "truehand_task_new", "truehand_task_move", "truehand_gate_check", "truehand_plan_fetch", "truehand_plan_check", "truehand_hand_run", "truehand_test_proof", "truehand_eyes", "truehand_review"} {
		if !names[want] {
			t.Errorf("missing tool %s", want)
		}
	}
	for n := range names {
		if strings.Contains(n, "approve") {
			t.Fatalf("agents must not have an approve tool: %s", n)
		}
	}

	if out, isErr := call(t, cs, "truehand_task_new", map[string]any{"id": "T-1", "title": "x"}); isErr {
		t.Fatal(out)
	}
	out, isErr := call(t, cs, "truehand_task_move", map[string]any{"state": "approved"})
	if isErr || !strings.Contains(out, `"moved": false`) || !strings.Contains(out, "no plan.md to approve") {
		t.Fatalf("gate must stay closed without a human: %s", out)
	}
	out, isErr = call(t, cs, "truehand_hand_run", map[string]any{"name": "check"})
	if isErr || !strings.Contains(out, `"verdict": "fail"`) {
		t.Fatalf("hand run: %s", out)
	}
	os.WriteFile(filepath.Join(dir, "ok.txt"), []byte("x"), 0o644)
	out, _ = call(t, cs, "truehand_hand_run", map[string]any{"name": "check"})
	if !strings.Contains(out, `"verdict": "pass"`) {
		t.Fatalf("hand run: %s", out)
	}
	out, _ = call(t, cs, "truehand_status", map[string]any{})
	var v core.TaskView
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.Task.ID != "T-1" || len(v.Evidence) != 1 {
		t.Fatalf("status: %v %s", err, out)
	}
	if out, isErr := call(t, cs, "truehand_review", map[string]any{"verdict": "pass", "notes": "short"}); !isErr {
		t.Fatalf("short review must be rejected: %s", out)
	}
	if out, isErr := call(t, cs, "truehand_task_move", map[string]any{"state": "nope"}); !isErr {
		t.Fatalf("bad state must be a tool error: %s", out)
	}
}
