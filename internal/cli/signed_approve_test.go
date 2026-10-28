package cli

import (
	"strings"
	"testing"

	"github.com/spaghetticoders/truehand/internal/sign/signtest"
)

func TestApproveRefusedWhenApproversListed(t *testing.T) {
	s := newSandbox(t)
	_, pub := signtest.Key(t)
	s.write(".truehand/config.yaml", testConfig+"approvers:\n  - id: mac\n    name: Emanuel\n    public_key: |\n      "+strings.ReplaceAll(strings.TrimSpace(pub), "\n", "\n      ")+"\n")
	s.git("add", ".")
	s.git("commit", "-q", "-m", "chore: approvers")
	s.must("task", "new", "T-1", "--title", "x")
	s.write(".truehand/tasks/T-1/plan.md", validPlan)
	withTTY(t, true)
	code, out := s.run("T-1\n", "approve")
	if code == 0 || !strings.Contains(out, "lists approvers") {
		t.Fatalf("terminal approval with approvers: %d\n%s", code, out)
	}
	if out := s.must("task", "show", "T-1"); strings.Contains(out, "approved by") {
		t.Fatalf("approval recorded:\n%s", out)
	}
}

func TestApproveRefusedWithSessionEnv(t *testing.T) {
	s := newSandbox(t)
	s.must("init", "--stack", "generic")
	s.write(".truehand/config.yaml", testConfig)
	s.must("task", "new", "T-1", "--title", "x")
	s.write(".truehand/tasks/T-1/plan.md", validPlan)
	withTTY(t, true)
	t.Setenv("TRUEHAND_SESSION", "s:x@127.0.0.1:1")
	code, out := s.run("T-1\n", "approve")
	if code == 0 || !strings.Contains(out, "Agents cannot approve") {
		t.Fatalf("approve from a detached agent: %d\n%s", code, out)
	}
}
