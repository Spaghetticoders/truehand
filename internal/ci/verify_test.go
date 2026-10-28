package ci

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/config"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/evidence"
	"github.com/spaghetticoders/truehand/internal/sign"
	"github.com/spaghetticoders/truehand/internal/sign/signtest"
)

func TestMain(m *testing.M) {
	// Tests may run inside an agent the app launched; they must not count as one.
	os.Unsetenv("TRUEHAND_SESSION")
	core.AgentDetector = func() int { return 0 }
	os.Unsetenv("TRUEHAND_ACTOR")
	os.Exit(m.Run())
}

const plan = "# T\n\n## Goal\ng\n\n## Scope\ns\n\n## Out of scope\no\n\n## Steps\n1. a — verify: b\n\n## Verification\nv\n\n## Risks\nr\n"

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, p, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

const gates = "version: 0\nbase: main\ngates:\n  approved:\n    - check: human_approval\n"

// branch is a repo whose main lists approver "mac", with a feature branch carrying task T-1,
// delivered, and approved by approve (committed with the work).
func branch(t *testing.T, approve func(ws *core.Workspace, sha string), tamper func(dir string, ws *core.Workspace)) *Report {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "demo")
	os.MkdirAll(dir, 0o755)
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "h@x.y"}, {"config", "user.name", "H"},
		{"config", "core.hooksPath", ".git/hooks"}, {"remote", "add", "origin", "https://github.com/spaghetticoders/demo.git"}} {
		git(t, dir, a...)
	}
	write(t, config.Path(dir), gates+"approvers:\n  - id: mac\n    name: Emanuel\n    public_key: |\n      "+strings.ReplaceAll(strings.TrimSpace(macPub), "\n", "\n      ")+"\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "chore: truehand")
	git(t, dir, "checkout", "-q", "-b", "feature")
	ws, err := core.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	task, err := ws.NewTask("T-1", "Demo", "")
	if err != nil {
		t.Fatal(err)
	}
	write(t, ws.Store.PlanPath("T-1"), plan)
	write(t, filepath.Join(dir, "a.txt"), "work\n")
	approve(ws, evidence.HashBytes([]byte(plan)))
	task.State = board.Delivered
	ws.Store.Save(task)
	if tamper != nil {
		tamper(dir, ws)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "feat: work")
	ws, err = core.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Verify(ws, "main")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

var macPub string

func TestVerifySignedApprovals(t *testing.T) {
	k, pub := signtest.Key(t)
	macPub = pub
	signed := func(ws *core.Workspace, sha string) {
		s := signtest.Sign(t, k, sign.Intent{
			Action: sign.ActionApprovePlan, Repo: "github.com/spaghetticoders/demo", Task: "T-1", PlanSHA256: sha,
			Nonce: sign.NewNonce(), Time: time.Now().UTC().Format("2006-01-02T15:04:05Z"), Approver: "mac",
		})
		ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha, Via: "signed", Sig: &s})
	}
	unsigned := func(ws *core.Workspace, sha string) {
		ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha, Via: "terminal"})
	}

	if r := branch(t, signed, nil); !r.Pass {
		t.Fatalf("signed approval refused: %v", r.Problems)
	}
	if r := branch(t, unsigned, nil); r.Pass {
		t.Fatal("unsigned approval passed CI although main lists approvers")
	}
	dropAndRedirect := func(dir string, ws *core.Workspace) {
		write(t, config.Path(dir), gates)
		task, _ := ws.Store.Load("T-1")
		task.BaseRef = ""
		ws.Store.Save(task)
	}
	if r := branch(t, unsigned, dropAndRedirect); r.Pass {
		t.Fatal("a branch dropping the approvers and the task's base_ref passed CI unsigned")
	}
	agent, agentPub := signtest.Key(t)
	ownKey := func(ws *core.Workspace, sha string) {
		s := signtest.Sign(t, agent, sign.Intent{
			Action: sign.ActionApprovePlan, Repo: "github.com/spaghetticoders/demo", Task: "T-1", PlanSHA256: sha,
			Nonce: sign.NewNonce(), Time: time.Now().UTC().Format("2006-01-02T15:04:05Z"), Approver: "agent",
		})
		ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha, Via: "signed", Sig: &s})
	}
	addKey := func(dir string, ws *core.Workspace) {
		write(t, config.Path(dir), gates+"approvers:\n  - id: agent\n    name: Agent\n    public_key: |\n      "+strings.ReplaceAll(strings.TrimSpace(agentPub), "\n", "\n      ")+"\n")
	}
	if r := branch(t, ownKey, addKey); r.Pass {
		t.Fatal("a branch listing its own key and signing with it passed CI")
	}
	redirect := func(dir string, ws *core.Workspace) {
		git(t, dir, "stash", "-q", "-u")
		git(t, dir, "checkout", "-q", "-b", "evil", "main")
		addKey(dir, ws)
		git(t, dir, "commit", "-q", "-am", "chore: my key")
		git(t, dir, "checkout", "-q", "feature")
		git(t, dir, "stash", "pop", "-q")
		task, _ := ws.Store.Load("T-1")
		task.BaseRef = "evil"
		ws.Store.Save(task)
	}
	if r := branch(t, ownKey, redirect); r.Pass {
		t.Fatal("a task pointing base_ref at a branch listing the agent's key passed CI")
	}
}
