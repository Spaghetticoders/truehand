package core

import (
	"crypto/ecdsa"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/config"
	"github.com/spaghetticoders/truehand/internal/evidence"
	"github.com/spaghetticoders/truehand/internal/gate"
	"github.com/spaghetticoders/truehand/internal/sign"
	"github.com/spaghetticoders/truehand/internal/sign/signtest"
)

const signedRepo = "github.com/spaghetticoders/demo"

func approversYAML(entries map[string]string) string {
	s := "approvers:\n"
	for id, key := range entries {
		s += "  - id: " + id + "\n    name: Name of " + id + "\n    public_key: |\n      " + strings.ReplaceAll(strings.TrimSpace(key), "\n", "\n      ") + "\n"
	}
	return s
}

// signedSetup is a repo whose committed config on main lists approver "mac", with task T-1
// planned and its plan complete.
func signedSetup(t *testing.T) (string, *Workspace, *ecdsa.PrivateKey, string) {
	t.Helper()
	k, pub := signtest.Key(t)
	dir := bareRepo(t)
	git(t, dir, "remote", "add", "origin", "git@github.com:Spaghetticoders/demo.git")
	write(t, config.Path(dir), testConfig+approversYAML(map[string]string{"mac": pub}))
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "chore: truehand")
	ws, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.NewTask("T-1", "Demo", ""); err != nil {
		t.Fatal(err)
	}
	write(t, ws.Store.PlanPath("T-1"), goodPlan)
	return dir, ws, k, evidence.HashBytes([]byte(goodPlan))
}

func approveIntent(sha string) sign.Intent {
	return sign.Intent{
		Action: sign.ActionApprovePlan, Repo: signedRepo, Task: "T-1", PlanSHA256: sha,
		Nonce: sign.NewNonce(), Time: timeNow().UTC().Format("2006-01-02T15:04:05Z"), Approver: "mac",
	}
}

func approvedGate(t *testing.T, ws *Workspace) (bool, string) {
	t.Helper()
	task, _ := ws.Store.Load("T-1")
	res, err := ws.Check(task, board.Approved)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range res.Items {
		if it.Check == "human_approval" {
			return it.Status == "pass", it.Message
		}
	}
	t.Fatal("no human_approval item")
	return false, ""
}

func TestApproveSigned(t *testing.T) {
	_, ws, k, sha := signedSetup(t)
	if ok, msg := approvedGate(t, ws); ok || !strings.Contains(msg, "approver") {
		t.Fatalf("before: %v %s", ok, msg)
	}
	s := signtest.Sign(t, k, approveIntent(sha))
	task, a, err := ws.ApproveSigned("T-1", s)
	if err != nil {
		t.Fatal(err)
	}
	if task.State != board.Approved || a.Name != "Name of mac" || a.Via != "signed:es256:mac" || a.Sig == nil {
		t.Fatalf("task %s approval %+v", task.State, a)
	}
	if ok, msg := approvedGate(t, ws); !ok || !strings.Contains(msg, "signed with key mac") {
		t.Fatalf("gate: %v %s", ok, msg)
	}
	events, _ := ws.Store.Events("T-1")
	found := false
	for _, e := range events {
		found = found || e.Type == "approved" && e.Actor == "approver:mac"
	}
	if !found {
		t.Fatal("no approved event by approver:mac")
	}
	if _, _, err := ws.ApproveSigned("T-1", s); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("replay: %v", err)
	}

	write(t, ws.Store.PlanPath("T-1"), goodPlan+"\nmore\n")
	if ok, msg := approvedGate(t, ws); ok || !strings.Contains(msg, "changed") {
		t.Fatalf("plan edited after signing: %v %s", ok, msg)
	}
}

func TestApproveSignedRefuses(t *testing.T) {
	dir, ws, k, sha := signedSetup(t)
	evil, evilPub := signtest.Key(t)
	// A key added only in the working tree is not on the base branch.
	cfg, _ := os.ReadFile(config.Path(dir))
	write(t, config.Path(dir), strings.Replace(string(cfg), "approvers:\n", approversYAML(map[string]string{"agent": evilPub}), 1))
	ws, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws.Config.Approvers) != 2 {
		t.Fatalf("setup: %+v", ws.Config.Approvers)
	}
	old := timeNow().Add(-10 * time.Minute).UTC().Format("2006-01-02T15:04:05Z")
	cases := map[string]sign.Signed{}
	for name, change := range map[string]func(*sign.Intent){
		"stale":       func(i *sign.Intent) { i.Time = old },
		"other task":  func(i *sign.Intent) { i.Task = "T-2" },
		"other plan":  func(i *sign.Intent) { i.PlanSHA256 = strings.Repeat("0", 64) },
		"other repo":  func(i *sign.Intent) { i.Repo = "github.com/evil/demo" },
		"unknown key": func(i *sign.Intent) { i.Approver = "nobody" },
		"push intent": func(i *sign.Intent) { i.Action, i.Head = sign.ActionPush, strings.Repeat("a", 40) },
	} {
		in := approveIntent(sha)
		change(&in)
		cases[name] = signtest.Sign(t, k, in)
	}
	in := approveIntent(sha)
	in.Approver = "agent"
	cases["key only in the working tree"] = signtest.Sign(t, evil, in)
	cases["signed by another key"] = signtest.Sign(t, evil, approveIntent(sha))
	for name, s := range cases {
		if _, _, err := ws.ApproveSigned("T-1", s); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if apps, _ := ws.Store.Approvals("T-1"); len(apps) != 0 {
		t.Fatalf("refused approvals recorded: %+v", apps)
	}

	// Recorded by hand, the same forgeries do not open the gate either.
	ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha, Via: "terminal"})
	s := cases["key only in the working tree"]
	ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha, Sig: &s})
	if ok, msg := approvedGate(t, ws); ok {
		t.Fatalf("gate opened: %s", msg)
	}
}

func TestSignedApprovalNonceOnce(t *testing.T) {
	_, ws, k, sha := signedSetup(t)
	s := signtest.Sign(t, k, approveIntent(sha))
	ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha, Sig: &s})
	ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha, Sig: &s})
	if ok, msg := approvedGate(t, ws); !ok {
		t.Fatalf("a copy of a signed approval voided the original: %s", msg)
	}
	write(t, ws.Store.PlanPath("T-1"), goodPlan+"\nv2\n")
	sha2 := evidence.HashBytes([]byte(goodPlan + "\nv2\n"))
	in := s.Intent
	in.PlanSHA256 = sha2
	reused := signtest.Sign(t, k, in)
	ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha2, Sig: &reused})
	if ok, msg := approvedGate(t, ws); ok || !strings.Contains(msg, "already used") {
		t.Fatalf("a nonce signed again for a new plan: %v %s", ok, msg)
	}
}

func TestSignedApprovalsNotRelaxedByBranch(t *testing.T) {
	dir, ws, _, sha := signedSetup(t)
	git(t, dir, "branch", "nobase", "HEAD~1")
	git(t, dir, "checkout", "-q", "-b", "feature")
	task, _ := ws.Store.Load("T-1")
	task.BaseRef = "nobase"
	ws.Store.Save(task)
	ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha, Via: "terminal"})
	if ok, msg := approvedGate(t, ws); ok {
		t.Fatalf("pointing base_ref at a branch without approvers relaxed the gate: %s", msg)
	}

	git(t, dir, "stash", "-q", "-u")
	git(t, dir, "checkout", "-q", "-b", "plainbase", "main")
	write(t, config.Path(dir), testConfig)
	git(t, dir, "commit", "-q", "-am", "chore: no approvers")
	git(t, dir, "checkout", "-q", "feature")
	git(t, dir, "stash", "pop", "-q")
	task, _ = ws.Store.Load("T-1")
	task.BaseRef = "plainbase"
	ws.Store.Save(task)
	if ok, msg := approvedGate(t, ws); ok {
		t.Fatalf("pointing base_ref at a branch whose config lists no approvers relaxed the gate: %s", msg)
	}

	write(t, config.Path(dir), testConfig)
	ws, _ = Open(dir)
	if ok, msg := approvedGate(t, ws); ok {
		t.Fatalf("dropping the working list too relaxed the gate: %s", msg)
	}
	ws.ApproversRef = git(t, dir, "rev-parse", "main")
	if ok, msg := approvedGate(t, ws); ok {
		t.Fatalf("CI reading the approvers at the merge-base accepted an unsigned approval: %s", msg)
	}
}

func TestNoApprovers(t *testing.T) {
	_, ws := repo(t)
	ws.NewTask("T-1", "x", "")
	write(t, ws.Store.PlanPath("T-1"), goodPlan)
	k, _ := signtest.Key(t)
	s := signtest.Sign(t, k, approveIntent(evidence.HashBytes([]byte(goodPlan))))
	if _, _, err := ws.ApproveSigned("T-1", s); !errors.Is(err, ErrNoApprovers) {
		t.Fatalf("%v", err)
	}
}

func TestVerifyIntent(t *testing.T) {
	dir, ws, k, sha := signedSetup(t)
	task, _ := ws.Store.Load("T-1")
	head := git(t, dir, "rev-parse", "HEAD")
	push := func(change func(*sign.Intent)) sign.Signed {
		in := approveIntent(sha)
		in.Action, in.Head = sign.ActionPush, head
		if change != nil {
			change(&in)
		}
		return signtest.Sign(t, k, in)
	}
	s := push(nil)
	ap, err := ws.VerifyIntent(task, sign.ActionPush, head, s)
	if err != nil || ap.ID != "mac" {
		t.Fatalf("%v %v", ap, err)
	}
	if _, err := ws.VerifyIntent(task, sign.ActionPush, head, s); err == nil {
		t.Fatal("replayed push intent accepted")
	}
	for name, s := range map[string]sign.Signed{
		"other head":  push(func(i *sign.Intent) { i.Head = strings.Repeat("b", 40) }),
		"open-pr sig": push(func(i *sign.Intent) { i.Action = sign.ActionOpenPR }),
		"other plan":  push(func(i *sign.Intent) { i.PlanSHA256 = strings.Repeat("c", 64) }),
		"stale":       push(func(i *sign.Intent) { i.Time = "2020-01-01T00:00:00Z" }),
	} {
		if _, err := ws.VerifyIntent(task, sign.ActionPush, head, s); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := ws.VerifyIntent(task, sign.ActionApprovePlan, "", push(nil)); err == nil {
		t.Fatal("approve-plan through VerifyIntent")
	}
	gd, _ := ws.Repo.GitDir()
	if _, err := os.Stat(filepath.Join(gd, "truehand", "intents.jsonl")); err != nil {
		t.Fatal(err)
	}
}

func TestApproversIntroducedOnBranch(t *testing.T) {
	dir, _ := repo(t)
	git(t, dir, "remote", "add", "origin", "git@github.com:Spaghetticoders/demo.git")
	k, pub := signtest.Key(t)
	write(t, config.Path(dir), testConfig+approversYAML(map[string]string{"mac": pub}))
	ws, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ws.NewTask("T-1", "x", "")
	write(t, ws.Store.PlanPath("T-1"), goodPlan)
	sha := evidence.HashBytes([]byte(goodPlan))
	ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha, Via: "terminal"})
	if ok, msg := approvedGate(t, ws); ok {
		t.Fatalf("unsigned approval with approvers in the config: %s", msg)
	}
	s := signtest.Sign(t, k, approveIntent(sha))
	ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha, Sig: &s})
	if ok, msg := approvedGate(t, ws); !ok {
		t.Fatalf("approvers introduced on the branch, nowhere else yet: %s", msg)
	}
}

func TestRepoIDWithoutOrigin(t *testing.T) {
	dir, ws := repo(t)
	root := git(t, dir, "rev-list", "--max-parents=0", "HEAD")
	if id := gate.RepoID(ws.Repo); id != "root:"+root {
		t.Fatalf("repo id %q", id)
	}
	// Repos made in the same second from the same content share a root commit, as clones do.
	dir2 := bareRepo(t)
	git(t, dir2, "commit", "-q", "--amend", "--allow-empty", "-m", "chore: another repo")
	write(t, config.Path(dir2), testConfig)
	ws2, err := Open(dir2)
	if err != nil {
		t.Fatal(err)
	}
	if gate.RepoID(ws2.Repo) == gate.RepoID(ws.Repo) {
		t.Fatal("two repos without origin share an identity")
	}

	k, pub := signtest.Key(t)
	write(t, config.Path(dir), testConfig+approversYAML(map[string]string{"mac": pub}))
	git(t, dir, "commit", "-q", "-am", "chore: approvers")
	ws, _ = Open(dir)
	ws.NewTask("T-1", "x", "")
	write(t, ws.Store.PlanPath("T-1"), goodPlan)
	in := approveIntent(evidence.HashBytes([]byte(goodPlan)))
	in.Repo = gate.RepoID(ws.Repo)
	if _, _, err := ws.ApproveSigned("T-1", signtest.Sign(t, k, in)); err != nil {
		t.Fatalf("repo without origin: %v", err)
	}
	in.Repo, in.Nonce = gate.RepoID(ws2.Repo), sign.NewNonce()
	if _, _, err := ws.ApproveSigned("T-1", signtest.Sign(t, k, in)); err == nil {
		t.Fatal("a signature for another origin-less repo accepted")
	}
}

func TestForgedEarlierNonceDoesNotVoid(t *testing.T) {
	_, ws, k, sha := signedSetup(t)
	other, _ := signtest.Key(t)
	genuine := signtest.Sign(t, k, approveIntent(sha))
	forged := signtest.Sign(t, other, genuine.Intent)
	ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha, Sig: &forged})
	ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha, Sig: &genuine})
	if ok, msg := approvedGate(t, ws); !ok {
		t.Fatalf("a forged record with the same nonce, written first, voided the genuine one: %s", msg)
	}
}

func TestApproversFromEveryBaseSource(t *testing.T) {
	dir, ws, k, sha := signedSetup(t)
	phone, phonePub := signtest.Key(t)
	git(t, dir, "checkout", "-q", "-b", "release")
	write(t, config.Path(dir), testConfig+approversYAML(map[string]string{"phone": phonePub}))
	git(t, dir, "commit", "-q", "-am", "chore: phone key")
	git(t, dir, "checkout", "-q", "main")
	task, _ := ws.Store.Load("T-1")
	task.BaseRef = "release"
	ws.Store.Save(task)
	for _, c := range []struct {
		key *ecdsa.PrivateKey
		id  string
	}{{k, "mac"}, {phone, "phone"}} {
		in := approveIntent(sha)
		in.Approver = c.id
		s := signtest.Sign(t, c.key, in)
		ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha, Sig: &s})
		if ok, msg := approvedGate(t, ws); !ok {
			t.Fatalf("key %s of a base source refused: %s", c.id, msg)
		}
	}

	task.BaseRef = "gone"
	ws.Store.Save(task)
	write(t, ws.Store.PlanPath("T-1"), goodPlan+"\nv2\n")
	sha2 := evidence.HashBytes([]byte(goodPlan + "\nv2\n"))
	in := approveIntent(sha2)
	in.Approver = "phone"
	s := signtest.Sign(t, phone, in)
	ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha2, Via: "os-auth:macos"})
	ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha2, Sig: &s})
	if ok, msg := approvedGate(t, ws); ok {
		t.Fatalf("base ref gone: its key no longer counts and signing is still required, yet: %s", msg)
	}
}

func TestNoBaseResolves(t *testing.T) {
	_, ws, _, _ := signedSetup(t)
	task, _ := ws.Store.Load("T-1")
	task.BaseRef, task.Base = "gone", strings.Repeat("1", 40)
	ws.Store.Save(task)
	ws.Config.Base = "also-gone"
	git(t, ws.Repo.Root, "branch", "-m", "main", "trunk")
	if _, err := gate.BaseApprovers(ws.Repo, ws.Config, task, ""); err == nil || !strings.Contains(err.Error(), "resolves") {
		t.Fatalf("nothing resolves: %v", err)
	}
}

func TestStartCommitStillRequiresSignature(t *testing.T) {
	dir, _, _, sha := signedSetup(t)
	write(t, config.Path(dir), "version: 0\ngates:\n  approved:\n    - check: plan_valid\n    - check: human_approval\n")
	ws, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	task, _ := ws.Store.Load("T-1")
	task.BaseRef = ""
	ws.Store.Save(task)
	ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha, Via: "os-auth:macos"})
	if ok, msg := approvedGate(t, ws); ok || !strings.Contains(msg, "approver") {
		t.Fatalf("no list, no base, no base_ref: the start commit still lists approvers, yet: %s", msg)
	}
}

func TestLocalMainIsAnAnchor(t *testing.T) {
	dir, _, _, sha := signedSetup(t)
	git(t, dir, "checkout", "-q", "-b", "feature")
	empty := git(t, dir, "commit-tree", git(t, dir, "hash-object", "-t", "tree", "/dev/null"), "-m", "x")
	write(t, config.Path(dir), "version: 0\ngates:\n  approved:\n    - check: plan_valid\n    - check: human_approval\n")
	ws, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	task, _ := ws.Store.Load("T-1")
	task.BaseRef, task.Base = "", empty
	ws.Store.Save(task)
	ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha, Via: "os-auth:macos"})
	if ok, msg := approvedGate(t, ws); ok || !strings.Contains(msg, "approver") {
		t.Fatalf("no origin, start commit rewritten: local main still lists approvers, yet: %s", msg)
	}
}

func TestNoBaseNoApproversStillApproves(t *testing.T) {
	dir := bareRepo(t)
	write(t, config.Path(dir), "version: 0\ngates:\n  approved:\n    - check: human_approval\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "chore: truehand")
	git(t, dir, "branch", "-m", "main", "trunk")
	ws, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ws.NewTask("T-1", "x", "")
	write(t, ws.Store.PlanPath("T-1"), goodPlan)
	ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: evidence.HashBytes([]byte(goodPlan)), Via: "os-auth:macos"})
	if ok, msg := approvedGate(t, ws); !ok {
		t.Fatalf("a repo with no base and no approvers lost its approvals: %s", msg)
	}
}

func TestSessionApproversPerTask(t *testing.T) {
	dir, ws, k, sha := signedSetup(t)
	phone, phonePub := signtest.Key(t)
	git(t, dir, "checkout", "-q", "-b", "release")
	write(t, config.Path(dir), testConfig+approversYAML(map[string]string{"phone": phonePub}))
	git(t, dir, "commit", "-q", "-am", "chore: phone key")
	git(t, dir, "checkout", "-q", "main")
	ws.NewTask("T-2", "on release", "")
	t2, _ := ws.Store.Load("T-2")
	t2.BaseRef = "release"
	ws.Store.Save(t2)
	write(t, ws.Store.PlanPath("T-2"), goodPlan)
	s1 := signtest.Sign(t, k, approveIntent(sha))
	ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha, Sig: &s1})
	in := approveIntent(sha)
	in.Task, in.Approver = "T-2", "phone"
	s2 := signtest.Sign(t, phone, in)
	ws.Store.AppendApproval("T-2", board.Approval{State: board.Approved, PlanSHA: sha, Sig: &s2})
	bv, err := ws.Board()
	if err != nil {
		t.Fatal(err)
	}
	for _, bt := range bv.Tasks {
		if len(bt.Missing) > 0 {
			t.Fatalf("%s: gate approved misses %v (keys remembered for another task?)", bt.ID, bt.Missing)
		}
	}
}
