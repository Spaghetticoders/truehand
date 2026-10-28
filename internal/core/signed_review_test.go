package core

import (
	"strings"
	"testing"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/config"
	"github.com/spaghetticoders/truehand/internal/evidence"
	"github.com/spaghetticoders/truehand/internal/sign"
	"github.com/spaghetticoders/truehand/internal/sign/signtest"
)

func reopen(t *testing.T, dir string) *Workspace {
	t.Helper()
	ws, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

// A branch that adds its own key to the working config cannot approve with it.
func TestReviewSignedBranchKeyDoesNotCount(t *testing.T) {
	dir, _, _, sha := signedSetup(t)
	evil, evilPub := signtest.Key(t)
	write(t, config.Path(dir), testConfig+approversYAML(map[string]string{"evil": evilPub}))
	ws := reopen(t, dir)
	in := approveIntent(sha)
	in.Approver = "evil"
	s := signtest.Sign(t, evil, in)
	if _, _, err := ws.ApproveSigned("T-1", s); err == nil {
		t.Fatal("ApproveSigned accepted a key the base branch does not list")
	}
	ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha, Sig: &s, Nonce: in.Nonce})
	if ok, msg := approvedGate(t, ws); ok {
		t.Fatalf("gate passed with the branch's own key: %s", msg)
	}
}

// Dropping the approvers from the working config and pointing the task's base_ref nowhere
// (or removing it) must not fall back to an unsigned approval.
func TestReviewSignedNoFallbackToUnsigned(t *testing.T) {
	for _, baseRef := range []string{"no-such-branch", ""} {
		t.Run("base_ref="+baseRef, func(t *testing.T) {
			dir, _, _, sha := signedSetup(t)
			write(t, config.Path(dir), strings.Replace(testConfig, "base: main\n", "", 1))
			ws := reopen(t, dir)
			tk, _ := ws.Store.Load("T-1")
			tk.BaseRef = baseRef
			if err := ws.Store.Save(tk); err != nil {
				t.Fatal(err)
			}
			ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha, Name: "forged", Via: "os-auth:macos"})
			if ok, msg := approvedGate(t, ws); ok {
				t.Fatalf("an unsigned approval passes once the list is dropped and base_ref is %q: %s", baseRef, msg)
			}
		})
	}
}

func TestReviewSignedUnsignedApprovalRefused(t *testing.T) {
	_, ws, _, sha := signedSetup(t)
	ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha, Name: "forged", Via: "os-auth:macos"})
	if ok, msg := approvedGate(t, ws); ok {
		t.Fatalf("unsigned approval passed: %s", msg)
	}
}

func TestReviewSignedBindings(t *testing.T) {
	_, ws, k, sha := signedSetup(t)
	for name, mut := range map[string]func(*sign.Intent){
		"other repo":   func(i *sign.Intent) { i.Repo = "github.com/spaghetticoders/other" },
		"other task":   func(i *sign.Intent) { i.Task = "T-2" },
		"other plan":   func(i *sign.Intent) { i.PlanSHA256 = strings.Repeat("a", 64) },
		"other action": func(i *sign.Intent) { i.Action = "push"; i.Head = strings.Repeat("b", 40) },
		"stale":        func(i *sign.Intent) { i.Time = "2020-01-01T00:00:00Z" },
	} {
		in := approveIntent(sha)
		mut(&in)
		if _, _, err := ws.ApproveSigned("T-1", signtest.Sign(t, k, in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	in := approveIntent(sha)
	s := signtest.Sign(t, k, in)
	s.Intent.Approver = "mac"
	s.Intent.Nonce = strings.Repeat("c", 32)
	if _, _, err := ws.ApproveSigned("T-1", s); err == nil {
		t.Error("a field changed after signing was accepted")
	}
	if _, _, err := ws.ApproveSigned("T-1", signtest.Sign(t, k, in)); err != nil {
		t.Fatalf("genuine: %v", err)
	}
	if _, _, err := ws.ApproveSigned("T-1", signtest.Sign(t, k, in)); err == nil {
		t.Fatal("nonce reused")
	}
}

// A repo that never listed approvers keeps its unsigned approvals working even when the base
// ref cannot be resolved (origin not fetched yet).
func TestReviewNoApproversUnresolvedBaseStillApproves(t *testing.T) {
	dir, ws := repo(t)
	if _, err := ws.NewTask("T-1", "Demo", ""); err != nil {
		t.Fatal(err)
	}
	write(t, ws.Store.PlanPath("T-1"), goodPlan)
	write(t, config.Path(dir), strings.Replace(testConfig, "base: main", "base: origin/main", 1))
	ws = reopen(t, dir)
	tk, _ := ws.Store.Load("T-1")
	tk.BaseRef = "origin/main"
	ws.Store.Save(tk)
	sha := evidence.HashBytes([]byte(goodPlan))
	ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha, Name: "H", Via: "os-auth:macos"})
	if ok, msg := approvedGate(t, ws); !ok {
		t.Fatalf("a repo without approvers lost its approvals because origin/main is not fetched: %s", msg)
	}
}
