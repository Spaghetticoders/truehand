package serve

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Committing the config puts its gates and approvers on the base branch, where CI trusts
// them. The person must commit exactly the config they were shown: one an agent rewrote in
// the meantime (say, adding its own approver key) must not ride on their Touch ID.
func TestReviewCommitConfigIsBoundToWhatWasShown(t *testing.T) {
	e := newEnv(t, false)
	e.s.Run = runCmd
	dir := filepath.Join(t.TempDir(), "fresh")
	os.MkdirAll(filepath.Join(dir, ".truehand"), 0o755)
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "config", "user.email", "h@x.y")
	git(t, dir, "config", "user.name", "H")
	os.WriteFile(filepath.Join(dir, "README"), []byte("x"), 0o644)
	git(t, dir, "add", "README")
	git(t, dir, "commit", "-q", "-m", "chore: init")
	shown := "version: 0\nbase: main\ngates:\n  approved:\n    - check: plan_valid\n    - check: human_approval\n"
	cfg := filepath.Join(dir, ".truehand/config.yaml")
	os.WriteFile(cfg, []byte(shown), 0o644)
	var p projectView
	if code := e.json("POST", "/api/projects", `{"path":"`+dir+`","clientName":"Acme"}`, asToken, &p); code != 201 {
		t.Fatalf("project: %d", code)
	}
	sum := sha256.Sum256([]byte(shown))
	_, agentPub := newKey(t)
	os.WriteFile(cfg, []byte(shown+"approvers:\n  - id: agent\n    name: Agent\n    public_key: |\n      "+strings.ReplaceAll(strings.TrimSpace(agentPub), "\n", "\n      ")+"\n"), 0o644)
	e.req("POST", "/api/projects/"+p.ID+"/commit-config", `{"configSha256":"`+hex.EncodeToString(sum[:])+`"}`, asShell)
	if out := git(t, dir, "log", "-p", "--all", "--", ".truehand/config.yaml"); strings.Contains(out, "id: agent") {
		t.Fatal("the person's Touch ID committed a config they were never shown, with an agent's approver key in it")
	}
}
