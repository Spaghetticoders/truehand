package ci

import (
	"os"
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

func approversBlock(keys ...[2]string) string {
	s := "approvers:\n"
	for _, k := range keys {
		s += "  - id: " + k[0] + "\n    name: " + k[0] + "\n    public_key: |\n      " + strings.ReplaceAll(strings.TrimSpace(k[1]), "\n", "\n      ") + "\n"
	}
	return s
}

// A branch made before a key was revoked on main is judged by main's tip: its approval signed
// with the revoked key no longer passes CI, and must be signed again.
func TestRevocationOnBaseTipHoldsForOlderBranches(t *testing.T) {
	mac, macKey := signtest.Key(t)
	phone, phoneKey := signtest.Key(t)
	run := func(signer string, revoke bool) *Report {
		dir := filepath.Join(t.TempDir(), "demo")
		os.MkdirAll(dir, 0o755)
		for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "h@x.y"}, {"config", "user.name", "H"},
			{"config", "core.hooksPath", ".git/hooks"}, {"remote", "add", "origin", "https://github.com/spaghetticoders/demo.git"}} {
			git(t, dir, a...)
		}
		write(t, config.Path(dir), gates+approversBlock([2]string{"mac", macKey}, [2]string{"phone", phoneKey}))
		git(t, dir, "add", ".")
		git(t, dir, "commit", "-q", "-m", "chore: truehand")
		git(t, dir, "checkout", "-q", "-b", "feature")
		ws, err := core.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		task, _ := ws.NewTask("T-1", "Demo", "")
		write(t, ws.Store.PlanPath("T-1"), plan)
		write(t, filepath.Join(dir, "a.txt"), "work\n")
		k := mac
		if signer == "phone" {
			k = phone
		}
		sha := evidence.HashBytes([]byte(plan))
		s := signtest.Sign(t, k, sign.Intent{
			Action: sign.ActionApprovePlan, Repo: "github.com/spaghetticoders/demo", Task: "T-1", PlanSHA256: sha,
			Nonce: sign.NewNonce(), Time: time.Now().UTC().Format("2006-01-02T15:04:05Z"), Approver: signer,
		})
		ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha, Via: "signed", Sig: &s})
		task.State = board.Delivered
		ws.Store.Save(task)
		git(t, dir, "add", "-A")
		git(t, dir, "commit", "-q", "-m", "feat: work")
		if revoke {
			git(t, dir, "checkout", "-q", "main")
			write(t, config.Path(dir), gates+approversBlock([2]string{"mac", macKey}))
			git(t, dir, "commit", "-q", "-am", "chore: phone lost, revoked")
			git(t, dir, "checkout", "-q", "feature")
		}
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
	if r := run("phone", false); !r.Pass {
		t.Fatalf("phone before the revocation refused: %v", r.Problems)
	}
	if r := run("phone", true); r.Pass {
		t.Fatal("a branch made before the revocation passed CI with the revoked key")
	}
	if r := run("mac", true); !r.Pass {
		t.Fatalf("a key still on main's tip refused: %v", r.Problems)
	}
}

// A shallow checkout without the base's history fails with the cause and the fix, not git's
// own messages.
func TestVerifyShallowCheckout(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	os.MkdirAll(dir, 0o755)
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "h@x.y"}, {"config", "user.name", "H"}} {
		git(t, dir, a...)
	}
	write(t, config.Path(dir), gates)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "chore: truehand")
	git(t, dir, "checkout", "-q", "-b", "feature")
	write(t, filepath.Join(dir, "a.txt"), "1\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "feat: a")
	write(t, filepath.Join(dir, "b.txt"), "2\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "feat: b")
	clone := filepath.Join(t.TempDir(), "c")
	git(t, dir, "clone", "-q", "--depth", "1", "--branch", "feature", "--no-single-branch", "file://"+dir, clone)
	git(t, clone, "fetch", "-q", "--depth", "1", "origin", "main:main")
	ws, err := core.Open(clone)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Verify(ws, "main")
	if err == nil {
		t.Fatal("shallow checkout verified")
	}
	if msg := err.Error(); !strings.Contains(msg, "the CI checkout is shallow: set fetch-depth: 0 in actions/checkout") || strings.Contains(strings.ToLower(msg), "graft") || strings.Contains(msg, "hint:") {
		t.Fatalf("message: %s", msg)
	}
}
