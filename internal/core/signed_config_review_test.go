package core

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/config"
	"github.com/spaghetticoders/truehand/internal/evidence"
	"github.com/spaghetticoders/truehand/internal/sign"
	"github.com/spaghetticoders/truehand/internal/sign/signtest"
)

// commitConfig rewrites the config with this tail, commits it and returns the new head and
// the config's sha256.
func commitConfig(t *testing.T, dir, tail, msg string) (string, string) {
	t.Helper()
	cfg := testConfig + tail
	write(t, config.Path(dir), cfg)
	git(t, dir, "add", config.Path(dir))
	git(t, dir, "commit", "-q", "-m", msg)
	return git(t, dir, "rev-parse", "HEAD"), evidence.HashBytes([]byte(cfg))
}

func committedKey(t *testing.T, ws *Workspace, commit string) string {
	t.Helper()
	b, _, _ := ws.Repo.BlobAt(commit, config.Dir+"/"+config.File)
	c, err := config.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return c.Approvers[0].PublicKey
}

// A push-base intent signs the config at head; the commits between the remote tip and head
// must not carry anything else onto the base (a workflow that disables the CI lock, code that
// skipped review).
func TestReviewPushBaseCarriesOnlyTheConfig(t *testing.T) {
	dir, ws, k, _ := signedSetup(t)
	from := git(t, dir, "rev-parse", "HEAD")
	pub := committedKey(t, ws, from)
	write(t, filepath.Join(dir, ".github", "workflows", "truehand.yml"), "on: push\njobs: {}\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "ci: nothing to see")
	head, sha := commitConfig(t, dir, "# v2\n"+approversYAML(map[string]string{"mac": pub}), "chore: config v2")
	s := signtest.Sign(t, k, configIntent(sign.ActionPushBase, sha, head))
	if _, err := ws.VerifyPushBase(from, head, s); err == nil {
		t.Fatal("push-base signed for the config also pushed a commit that changes .github/workflows")
	}
}

// What push-base puts on the base becomes the config every later approval is read from: one
// that does not parse locks every signed approval out.
func TestReviewPushBaseRefusesABrokenConfig(t *testing.T) {
	dir, ws, k, _ := signedSetup(t)
	from := git(t, dir, "rev-parse", "HEAD")
	head, sha := commitConfig(t, dir, "approvers:\n  - id: mac\n    name: x\n    public_key: not a key\n", "chore: broken")
	s := signtest.Sign(t, k, configIntent(sign.ActionPushBase, sha, head))
	if _, err := ws.VerifyPushBase(from, head, s); err == nil {
		t.Fatal("push-base accepted a config that does not parse")
	}
}

// Every config action's signature is bound to its action: one never passes for the other,
// nor for a task's push.
func TestReviewConfigIntentsAreNotInterchangeable(t *testing.T) {
	dir, ws, k, _ := signedSetup(t)
	base := git(t, dir, "rev-parse", "HEAD")
	pub := committedKey(t, ws, base)
	head, sha := commitConfig(t, dir, "# v2\n"+approversYAML(map[string]string{"mac": pub}), "chore: v2")

	cc := signtest.Sign(t, k, configIntent(sign.ActionCommitConfig, sha, head))
	if _, err := ws.VerifyPushBase(base, head, cc); err == nil {
		t.Error("commit-config signature pushed the base")
	}
	pb := signtest.Sign(t, k, configIntent(sign.ActionPushBase, sha, base))
	if _, err := ws.VerifyCommitConfig(base, sha, pb); err == nil {
		t.Error("push-base signature committed a config")
	}
	// Relabelled: the signature is over the message, so changing the action breaks it.
	relabel := signtest.Sign(t, k, configIntent(sign.ActionCommitConfig, sha, base))
	relabel.Intent.Action = sign.ActionPushBase
	relabel.Intent.Head = head
	if _, err := ws.VerifyPushBase(base, head, relabel); err == nil {
		t.Error("relabelled commit-config signature pushed the base")
	}
	task, _ := ws.Store.Load("T-1")
	pushIn := configIntent(sign.ActionPush, sha, head)
	pushIn.Task = "T-1"
	if _, err := ws.VerifyCommitConfig(base, sha, signtest.Sign(t, k, pushIn)); err == nil {
		t.Error("a task's push signature committed a config")
	}
	cfgAsPush := signtest.Sign(t, k, configIntent(sign.ActionPushBase, sha, head))
	if _, err := ws.VerifyIntent(task, sign.ActionPush, head, cfgAsPush); err == nil {
		t.Error("push-base signature pushed a task")
	}
}

// The approvers of a config action are those committed at the commit the caller names, never
// the working tree's, and a key added by the config being committed does not sign for it.
func TestReviewNewKeyCannotSignItsOwnArrival(t *testing.T) {
	dir, ws, _, _ := signedSetup(t)
	from := git(t, dir, "rev-parse", "HEAD")
	agent, agentPub := signtest.Key(t)
	head, sha := commitConfig(t, dir, approversYAML(map[string]string{"agent": agentPub}), "chore: my key")
	in := configIntent(sign.ActionPushBase, sha, head)
	in.Approver = "agent"
	if _, err := ws.VerifyPushBase(from, head, signtest.Sign(t, agent, in)); err == nil {
		t.Fatal("a key the pushed config adds signed its own push")
	}
	ws = reopen(t, dir)
	in = configIntent(sign.ActionCommitConfig, sha, from)
	in.Approver = "agent"
	if _, err := ws.VerifyCommitConfig(from, sha, signtest.Sign(t, agent, in)); err == nil {
		t.Fatal("a key in the working tree signed a config commit")
	}
}

// After a key rotation the old commit and the new tip both list "mac", with different keys:
// the current key must still sign.
func TestReviewRotatedKeyStillSigns(t *testing.T) {
	dir, ws, _, sha := signedSetup(t)
	start := git(t, dir, "rev-parse", "HEAD")
	task, _ := ws.Store.Load("T-1")
	task.Base = start
	ws.Store.Save(task)
	newK, newPub := signtest.Key(t)
	commitConfig(t, dir, approversYAML(map[string]string{"mac": newPub}), "chore: new phone")
	task.BaseRef = "main"
	task.Base = start
	ws.Store.Save(task)
	ws = reopen(t, dir)
	s := signtest.Sign(t, newK, approveIntent(sha))
	ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha, Sig: &s})
	if ok, msg := approvedGate(t, ws); !ok {
		t.Fatalf("the rotated key of approver mac on main was refused: %s", msg)
	}
}

// Two requests carrying the same signed intent at once: only one may spend it.
func TestReviewNonceSpentOnceUnderRace(t *testing.T) {
	dir, ws, k, _ := signedSetup(t)
	base := git(t, dir, "rev-parse", "HEAD")
	sha := strings.Repeat("c", 64)
	s := signtest.Sign(t, k, configIntent(sign.ActionCommitConfig, sha, base))
	var ok atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := ws.VerifyCommitConfig(base, sha, s); err == nil {
				ok.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if n := ok.Load(); n != 1 {
		t.Fatalf("one signed intent authorised %d times", n)
	}
}

// git replace makes every local git command see another object under head's id, while push
// sends the real one: the "only the config" check must look at the real commits.
func TestReviewPushBaseSeesThroughGitReplace(t *testing.T) {
	dir, ws, k, _ := signedSetup(t)
	from := git(t, dir, "rev-parse", "HEAD")
	pub := committedKey(t, ws, from)
	tail := "# v2\n" + approversYAML(map[string]string{"mac": pub})
	write(t, filepath.Join(dir, ".github", "workflows", "truehand.yml"), "on: push\njobs: {}\n")
	git(t, dir, "add", ".")
	head, sha := commitConfig(t, dir, tail, "chore: config v2")
	git(t, dir, "reset", "-q", "--hard", from)
	clean, _ := commitConfig(t, dir, tail, "chore: config v2")
	git(t, dir, "replace", head, clean)
	s := signtest.Sign(t, k, configIntent(sign.ActionPushBase, sha, head))
	if _, err := ws.VerifyPushBase(from, head, s); err == nil {
		t.Fatal("push-base of a commit that changes .github/workflows accepted behind git replace")
	}
}

// Revoking an approver by deleting it from main: a task that started before the deletion
// still has the old commit as an anchor, and that older commit must not bring the id back.
func TestReviewRemovedApproverDoesNotComeBack(t *testing.T) {
	dir, ws, k, sha := signedSetup(t)
	macPub := committedKey(t, ws, git(t, dir, "rev-parse", "HEAD"))
	phone, phonePub := signtest.Key(t)
	commitConfig(t, dir, approversYAML(map[string]string{"mac": macPub, "phone": phonePub}), "chore: phone")
	start := git(t, dir, "rev-parse", "HEAD")
	commitConfig(t, dir, approversYAML(map[string]string{"mac": macPub}), "chore: phone lost, revoked")
	task, _ := ws.Store.Load("T-1")
	task.Base = start
	ws.Store.Save(task)
	ws = reopen(t, dir)
	in := approveIntent(sha)
	in.Approver = "phone"
	s := signtest.Sign(t, phone, in)
	ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha, Sig: &s})
	if ok, _ := approvedGate(t, ws); ok {
		t.Fatal("approver removed on main still signs through the task's start commit")
	}
	in = approveIntent(sha)
	s = signtest.Sign(t, k, in)
	ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, PlanSHA: sha, Sig: &s})
	if ok, msg := approvedGate(t, ws); !ok {
		t.Fatalf("mac refused: %s", msg)
	}
}

// A config turned into a symlink would make the base read another file as its config.
func TestReviewPushBaseRefusesASymlinkConfig(t *testing.T) {
	dir, ws, k, _ := signedSetup(t)
	from := git(t, dir, "rev-parse", "HEAD")
	os.Remove(config.Path(dir))
	if err := os.Symlink("../README.md", config.Path(dir)); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "README.md"), testConfig)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "chore: link")
	head := git(t, dir, "rev-parse", "HEAD")
	s := signtest.Sign(t, k, configIntent(sign.ActionPushBase, evidence.HashBytes([]byte("../README.md")), head))
	if _, err := ws.VerifyPushBase(from, head, s); err == nil {
		t.Fatal("push-base of a config turned into a symlink accepted")
	}
}

// A shallow clone of a repo that lists no approvers anywhere: ancestry cannot be trusted, but
// there are no keys to choose between, so a plain human approval must still count.
func TestReviewShallowCloneWithoutApproversStillApproves(t *testing.T) {
	src, _ := repo(t)
	git(t, src, "commit", "-q", "--allow-empty", "-m", "chore: second")
	dir := filepath.Join(t.TempDir(), "shallow")
	git(t, filepath.Dir(dir), "clone", "-q", "--depth", "1", "file://"+src, dir)
	git(t, dir, "config", "user.email", "h@x.y")
	git(t, dir, "config", "user.name", "Human Name")
	ws, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.NewTask("T-1", "Demo", ""); err != nil {
		t.Fatal(err)
	}
	write(t, ws.Store.PlanPath("T-1"), goodPlan)
	sha := evidence.HashBytes([]byte(goodPlan))
	ws.Store.AppendApproval("T-1", board.Approval{State: board.Approved, Name: "H", PlanSHA: sha, Via: "os-auth:macos"})
	if ok, msg := approvedGate(t, ws); !ok {
		t.Fatalf("shallow clone without approvers: human approval refused: %s", msg)
	}
}
