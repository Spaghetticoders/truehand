package hook

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/core"
)

func workspace(t *testing.T, cfg string) *core.Workspace {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "a@b.c"}, {"config", "user.name", "A"}, {"commit", "-q", "--allow-empty", "-m", "chore: init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	os.MkdirAll(filepath.Join(dir, ".truehand"), 0o755)
	os.WriteFile(filepath.Join(dir, ".truehand/config.yaml"), []byte(cfg), 0o644)
	ws, err := core.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

const cfg = "version: 0\nbase: main\ncommands:\n  check: \"false\"\ngates:\n  verified:\n    - check: hand_passed\n      with: { hand: check }\n"

func TestCheckBash(t *testing.T) {
	ws := workspace(t, cfg)
	root := ws.Repo.Root
	cases := []struct {
		cmd  string
		deny bool
	}{
		{"go test ./...", false},
		{"git status && git diff", false},
		{"git push origin main", true},
		{"git -C . push", true},
		{"FOO=1 git push", true},
		{"echo ok; git push --force", true},
		{"git commit -m 'feat: x'", true}, // no active task
		{"truehand approve T-1", true},
		{"/usr/local/bin/truehand approve", true},
		{"truehand task show", false},
		{"cat .truehand/tasks/T-1/evidence/index.jsonl", false},
		{"echo '{}' > .truehand/tasks/T-1/evidence/index.jsonl", true},
		{"echo x >> .truehand/tasks/T-1/approvals.jsonl", true},
		{"cp fake.log .truehand/tasks/T-1/evidence/check-1-output.log", true},
		{"rm -rf .truehand/tasks/T-1/evidence", true},
		{"sed -i '' s/fail/pass/ .truehand/tasks/T-1/evidence/index.jsonl", true},
		{"echo 'version: 0' > .truehand/config.yaml", true},
		{"echo hi > .truehand/tasks/T-1/plan.md", false},
		{"cat .truehand/config* 2>/dev/null", false},
		{"python3 - <<'EOF'\np='.truehand/config.yaml'\nopen(p,'w').write('x')\nEOF", true},
		{"node -e \"require('fs').writeFileSync('.truehand/tasks/T-1/evidence/index.jsonl','')\"", true},
		{"python3 -c 'print(1)' && cat .truehand/tasks/T-1/plan.md", false},
		{"python3 scripts/gen.py lib/x.dart", false},
		{"grep -n x .truehand/tasks/T-1/events.jsonl > /tmp/out.txt", false},
		{"cat x 2>/dev/null > .truehand/tasks/T-1/task.yaml", true},
		{"cat x >| .truehand/tasks/T-1/task.yaml", true},
		{"git config core.hooksPath /dev/null", true},
		{"echo 'git push' ", false},
		{"cat " + filepath.Join(root, "x") + " > " + filepath.Join(root, ".truehand/tasks/T-1/task.yaml"), true},
	}
	for _, c := range cases {
		d := CheckBash(ws, root, c.cmd)
		if d.Deny != c.deny {
			t.Errorf("CheckBash(%q) deny=%v, want %v (%s)", c.cmd, d.Deny, c.deny, d.Reason)
		}
	}
}

func TestCommitNeedsReviewedTask(t *testing.T) {
	ws := workspace(t, cfg)
	tk, err := ws.NewTask("T-1", "x", "")
	if err != nil {
		t.Fatal(err)
	}
	ws.SetActiveTask("T-1")
	if d := CheckBash(ws, ws.Repo.Root, "git commit -m 'feat: x'"); !d.Deny || !strings.Contains(d.Reason, "planned") {
		t.Fatalf("expected deny in planned, got %+v", d)
	}
	tk.State = board.Reviewed
	tk.Updated = time.Now()
	ws.Store.Save(tk)
	if d := CheckBash(ws, ws.Repo.Root, "git commit -m 'feat: x'"); d.Deny {
		t.Fatalf("expected allow in reviewed, got %+v", d)
	}
	if d := CheckBash(ws, ws.Repo.Root, "git commit --no-verify -m 'feat: x'"); !d.Deny {
		t.Fatal("expected --no-verify to be denied")
	}
}

func TestCheckWrite(t *testing.T) {
	ws := workspace(t, cfg)
	root := ws.Repo.Root
	for p, want := range map[string]bool{
		filepath.Join(root, ".truehand/tasks/T-1/evidence/x.log"): true,
		filepath.Join(root, ".truehand/tasks/T-1/task.yaml"):      true,
		filepath.Join(root, ".truehand/config.yaml"):              true,
		filepath.Join(root, ".truehand/tasks/T-1/plan.md"):        false,
		filepath.Join(root, "lib/main.dart"):                      false,
		"/etc/hosts":                                              false,
	} {
		if d := CheckWrite(ws, p); d.Deny != want {
			t.Errorf("CheckWrite(%s) deny=%v want %v", p, d.Deny, want)
		}
	}
}

func TestCheckStop(t *testing.T) {
	ws := workspace(t, cfg)
	if d := CheckStop(ws, false); d.Deny {
		t.Fatal("no task: stop must be allowed")
	}
	tk, _ := ws.NewTask("T-1", "x", "")
	ws.SetActiveTask("T-1")
	if d := CheckStop(ws, false); d.Deny {
		t.Fatal("planned task: stop must be allowed (waiting for approval is legit)")
	}
	tk.State = board.Building
	ws.Store.Save(tk)
	d := CheckStop(ws, false)
	if !d.Deny || !strings.Contains(d.Reason, "hand_passed") {
		t.Fatalf("building with closed gate: expected block, got %+v", d)
	}
	if d := CheckStop(ws, true); d.Deny {
		t.Fatal("stop_hook_active must never block twice")
	}
}

func TestCheckBashEvasions(t *testing.T) {
	ws := workspace(t, cfg)
	root := ws.Repo.Root
	for _, c := range []string{
		"cd .truehand/tasks/T-1 && echo x >> approvals.jsonl",
		`sh -c "echo x > .truehand/tasks/T-1/task.yaml"`,
		`bash -c "git push"`,
		"git checkout main -- .truehand",
		"git restore .truehand/config.yaml",
		"git apply patch.diff",
		"git -c core.hooksPath=/dev/null merge feat/x",
		"go run ./cmd/truehand approve T-1",
		`sh -c "sleep 1; truehand approve T-1"`,
		"ln -s /etc/hosts .truehand/tasks/T-1/context.md",
	} {
		if d := CheckBash(ws, root, c); !d.Deny {
			t.Errorf("CheckBash(%q) must deny", c)
		}
	}
	for _, c := range []string{"cd lib && go test ./...", `sh -c "go vet ./..."`, "git checkout -b feat/x", "truehand serve --read-only"} {
		if d := CheckBash(ws, root, c); d.Deny {
			t.Errorf("CheckBash(%q) must allow: %s", c, d.Reason)
		}
	}
}
