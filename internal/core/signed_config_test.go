package core

import (
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

func configIntent(action, sha, head string) sign.Intent {
	return sign.Intent{
		Action: action, Repo: signedRepo, Task: sign.ConfigTask, PlanSHA256: sha, Head: head,
		Nonce: sign.NewNonce(), Time: timeNow().UTC().Format("2006-01-02T15:04:05Z"), Approver: "mac",
	}
}

func TestVerifyCommitConfig(t *testing.T) {
	dir, ws, k, _ := signedSetup(t)
	base := git(t, dir, "rev-parse", "HEAD")
	sha := strings.Repeat("c", 64)
	s := signtest.Sign(t, k, configIntent(sign.ActionCommitConfig, sha, base))
	ap, err := ws.VerifyCommitConfig(base, sha, s)
	if err != nil || ap.ID != "mac" {
		t.Fatalf("%v %v", ap, err)
	}
	if _, err := ws.VerifyCommitConfig(base, sha, s); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("nonce spent twice: %v", err)
	}
	evil, _ := signtest.Key(t)
	other := strings.Repeat("d", 64)
	for name, s := range map[string]sign.Signed{
		"other config": signtest.Sign(t, k, configIntent(sign.ActionCommitConfig, other, base)),
		"other base":   signtest.Sign(t, k, configIntent(sign.ActionCommitConfig, sha, strings.Repeat("a", 40))),
		"push-base":    signtest.Sign(t, k, configIntent(sign.ActionPushBase, sha, base)),
		"other key":    signtest.Sign(t, evil, configIntent(sign.ActionCommitConfig, sha, base)),
		"stale": func() sign.Signed {
			in := configIntent(sign.ActionCommitConfig, sha, base)
			in.Time = timeNow().Add(-time.Hour).UTC().Format("2006-01-02T15:04:05Z")
			return signtest.Sign(t, k, in)
		}(),
	} {
		if _, err := ws.VerifyCommitConfig(base, sha, s); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestCommitConfigNeedsCommittedApprovers(t *testing.T) {
	dir, _ := repo(t)
	git(t, dir, "remote", "add", "origin", "git@github.com:Spaghetticoders/demo.git")
	k, pub := signtest.Key(t)
	base := git(t, dir, "rev-parse", "HEAD")
	write(t, config.Path(dir), testConfig+approversYAML(map[string]string{"mac": pub}))
	ws, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("c", 64)
	if _, err := ws.VerifyCommitConfig(base, sha, signtest.Sign(t, k, configIntent(sign.ActionCommitConfig, sha, base))); err != ErrNoApprovers {
		t.Fatalf("approvers from the working tree signed a config commit: %v", err)
	}
}

func TestVerifyPushBase(t *testing.T) {
	dir, ws, k, _ := signedSetup(t)
	from := git(t, dir, "rev-parse", "HEAD")
	cfg := testConfig + "# v2\n" + approversYAML(map[string]string{"mac": func() string {
		b, _, _ := ws.Repo.BlobAt(from, config.Dir+"/"+config.File)
		c, _ := config.Parse(b)
		return c.Approvers[0].PublicKey
	}()})
	write(t, config.Path(dir), cfg)
	git(t, dir, "add", config.Path(dir))
	git(t, dir, "commit", "-q", "-m", "chore: config v2")
	head := git(t, dir, "rev-parse", "HEAD")
	sha := evidence.HashBytes([]byte(cfg))
	s := signtest.Sign(t, k, configIntent(sign.ActionPushBase, sha, head))
	if _, err := ws.VerifyPushBase(from, head, s); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.VerifyPushBase(from, head, s); err == nil {
		t.Fatal("nonce spent twice")
	}
	for name, s := range map[string]sign.Signed{
		"config sha of the old commit": signtest.Sign(t, k, configIntent(sign.ActionPushBase, strings.Repeat("e", 64), head)),
		"commit-config intent":         signtest.Sign(t, k, configIntent(sign.ActionCommitConfig, sha, head)),
	} {
		if _, err := ws.VerifyPushBase(from, head, s); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// Backwards: from does not descend from head's child, so pushing from over head refuses.
	if _, err := ws.VerifyPushBase(head, from, signtest.Sign(t, k, configIntent(sign.ActionPushBase, sha, from))); err == nil || !strings.Contains(err.Error(), "descend") {
		t.Fatalf("push that rewinds the base: %v", err)
	}
}

// After a rotation on the base, the replaced key no longer signs, even though the commit the
// task started from still lists it.
func TestReplacedKeyNoLongerSigns(t *testing.T) {
	dir, ws, oldK, sha := signedSetup(t)
	start := git(t, dir, "rev-parse", "HEAD")
	_, newPub := signtest.Key(t)
	write(t, config.Path(dir), testConfig+approversYAML(map[string]string{"mac": newPub}))
	git(t, dir, "add", config.Path(dir))
	git(t, dir, "commit", "-q", "-m", "chore: new phone")
	task, _ := ws.Store.Load("T-1")
	task.BaseRef, task.Base = "main", start
	ws.Store.Save(task)
	ws, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := signtest.Sign(t, oldK, approveIntent(sha))
	ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha, Sig: &s})
	if ok, msg := approvedGate(t, ws); ok {
		t.Fatalf("the replaced key still signs: %s", msg)
	}
}

// Each commit a push-base carries must change only the config: one that adds a workflow and
// one that removes it again leave the net diff clean, and are still refused.
func TestPushBaseEveryCommitOnlyConfig(t *testing.T) {
	dir, ws, k, _ := signedSetup(t)
	from := git(t, dir, "rev-parse", "HEAD")
	key := committedKey(t, ws, from)
	write(t, filepath.Join(dir, ".github", "workflows", "off.yml"), "on: never\n")
	git(t, dir, "add", ".github")
	git(t, dir, "commit", "-q", "-m", "ci: off")
	git(t, dir, "rm", "-q", ".github/workflows/off.yml")
	git(t, dir, "commit", "-q", "-m", "ci: back")
	head, sha := commitConfig(t, dir, "# v2\n"+approversYAML(map[string]string{"mac": key}), "chore: config")
	if _, err := ws.VerifyPushBase(from, head, signtest.Sign(t, k, configIntent(sign.ActionPushBase, sha, head))); err == nil || !strings.Contains(err.Error(), "changes more than") {
		t.Fatalf("a commit touching a workflow rode along: %v", err)
	}

	// A merge commit is refused too, whatever it brings.
	dir, ws, k, _ = signedSetup(t)
	from = git(t, dir, "rev-parse", "HEAD")
	key = committedKey(t, ws, from)
	git(t, dir, "checkout", "-q", "-b", "side")
	commitConfig(t, dir, "# side\n"+approversYAML(map[string]string{"mac": key}), "chore: side")
	git(t, dir, "checkout", "-q", "-")
	git(t, dir, "merge", "-q", "--no-ff", "-m", "merge", "side")
	head = git(t, dir, "rev-parse", "HEAD")
	b, _, _ := ws.Repo.BlobAt(head, config.Dir+"/"+config.File)
	if _, err := ws.VerifyPushBase(from, head, signtest.Sign(t, k, configIntent(sign.ActionPushBase, evidence.HashBytes(b), head))); err == nil || !strings.Contains(err.Error(), "merge") {
		t.Fatalf("merge commit pushed: %v", err)
	}
}

// A pushed config that drops every approver would turn signed approvals off: refused.
func TestPushBaseKeepsApprovers(t *testing.T) {
	dir, ws, k, _ := signedSetup(t)
	from := git(t, dir, "rev-parse", "HEAD")
	head, sha := commitConfig(t, dir, "", "chore: no approvers")
	if _, err := ws.VerifyPushBase(from, head, signtest.Sign(t, k, configIntent(sign.ActionPushBase, sha, head))); err == nil || !strings.Contains(err.Error(), "removes every approver") {
		t.Fatalf("approvers dropped: %v", err)
	}
}

// The newest anchor's key wins even when an older anchor comes first in priority: the task's
// base ref still names the commit before the rotation.
func TestRotationNewestAnchorWins(t *testing.T) {
	dir, ws, oldK, sha := signedSetup(t)
	start := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "branch", "old-base", start)
	newK, newPub := signtest.Key(t)
	commitConfig(t, dir, approversYAML(map[string]string{"mac": newPub}), "chore: new phone")
	task, _ := ws.Store.Load("T-1")
	task.BaseRef, task.Base = "old-base", start
	ws.Store.Save(task)
	ws = reopen(t, dir)
	task, _ = ws.Store.Load("T-1")
	list, err := ws.Approvers(task)
	if err != nil || len(list) != 1 || list[0].PublicKey != newPub {
		t.Fatalf("approvers after rotation: %v %v", list, err)
	}
	old := signtest.Sign(t, oldK, approveIntent(sha))
	ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha, Sig: &old})
	if ok, msg := approvedGate(t, ws); ok {
		t.Fatalf("old key signed after rotation: %s", msg)
	}
	cur := signtest.Sign(t, newK, approveIntent(sha))
	ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha, Sig: &cur})
	if ok, msg := approvedGate(t, ws); !ok {
		t.Fatalf("new key refused: %s", msg)
	}
}

// With grafts or a shallow history, ancestry is not the real one: signed config actions and
// the approvers list refuse to answer.
func TestSignedChecksRefuseRewrittenHistory(t *testing.T) {
	dir, ws, k, _ := signedSetup(t)
	base := git(t, dir, "rev-parse", "HEAD")
	gd, _ := ws.Repo.GitDir()
	write(t, filepath.Join(gd, "info", "grafts"), base+"\n")
	sha := strings.Repeat("c", 64)
	if _, err := ws.VerifyCommitConfig(base, sha, signtest.Sign(t, k, configIntent(sign.ActionCommitConfig, sha, base))); err == nil || !strings.Contains(err.Error(), "grafts") {
		t.Fatalf("commit-config with grafts: %v", err)
	}
	ws = reopen(t, dir)
	task, _ := ws.Store.Load("T-1")
	if _, err := ws.Approvers(task); err == nil || !strings.Contains(err.Error(), "grafts") {
		t.Fatalf("approvers with grafts: %v", err)
	}
	if ok, msg := approvedGate(t, ws); ok || !strings.Contains(msg, "history is not the real one") {
		t.Fatalf("gate with grafts and approvers: %v %s", ok, msg)
	}
	// CI reads one ref; the same refusal applies there.
	if _, err := gate.BaseApprovers(ws.Repo, ws.Config, task, base); err == nil || !strings.Contains(err.Error(), "grafts") {
		t.Fatalf("CI approvers with grafts: %v", err)
	}
}

// A shallow clone of a repo with approvers refuses with a clear message; without approvers
// approvals work as before (TestReviewShallowCloneWithoutApproversStillApproves).
func TestShallowCloneWithApproversRefuses(t *testing.T) {
	dir, _, _, _ := signedSetup(t)
	write(t, filepath.Join(dir, "x.txt"), "x\n")
	git(t, dir, "add", "x.txt")
	git(t, dir, "commit", "-q", "-m", "chore: x")
	clone := filepath.Join(t.TempDir(), "c")
	git(t, dir, "clone", "-q", "--depth", "1", "file://"+dir, clone)
	write(t, config.Path(clone), testConfig+"# local\n")
	ws, err := Open(clone)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.NewTask("T-1", "Demo", ""); err != nil {
		t.Fatal(err)
	}
	task, _ := ws.Store.Load("T-1")
	if _, err := ws.Approvers(task); err == nil || !strings.Contains(err.Error(), "shallow") {
		t.Fatalf("shallow clone with approvers: %v", err)
	}
}

// In a depth-1 clone the task's base ref exists but has no merge-base with HEAD: the error
// names the shallow clone, not a missing ref.
func TestBaseOfShallowClone(t *testing.T) {
	dir, _ := repo(t)
	start := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "checkout", "-q", "-b", "th/T-1")
	ws, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ws.NewTask("T-1", "x", "")
	task, _ := ws.Store.Load("T-1")
	task.BaseRef, task.Base = "origin/main", start
	ws.Store.Save(task)
	write(t, filepath.Join(dir, "a.txt"), "1\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "feat: a")
	write(t, filepath.Join(dir, "b.txt"), "2\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "feat: b")
	git(t, dir, "checkout", "-q", "main")
	write(t, filepath.Join(dir, "m.txt"), "m\n")
	git(t, dir, "add", "m.txt")
	git(t, dir, "commit", "-q", "-m", "chore: main moves")
	clone := filepath.Join(t.TempDir(), "c")
	git(t, dir, "clone", "-q", "--depth", "1", "--no-single-branch", "--branch", "th/T-1", "file://"+dir, clone)
	cw, err := Open(clone)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := cw.Store.Load("T-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cw.BaseOf(ct); err == nil || !strings.Contains(err.Error(), "the clone is shallow: run `git fetch --unshallow`") {
		t.Fatalf("base of a task in a shallow clone: %v", err)
	}
}
