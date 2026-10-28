package serve

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/sign"
)

func newKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	return k, string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// withApprovers commits approvers (the Mac's key) to the base branch of root.
func withApprovers(t *testing.T, root, pub string) {
	indented := "      " + strings.ReplaceAll(strings.TrimSpace(pub), "\n", "\n      ")
	cfg := "version: 0\nbase: main\napprovers:\n  - id: mac\n    name: Emanuel\n    public_key: |\n" + indented + "\ngates:\n  approved:\n    - check: plan_valid\n    - check: human_approval\n"
	os.WriteFile(filepath.Join(root, ".truehand/config.yaml"), []byte(cfg), 0o644)
	git(t, root, "add", ".truehand/config.yaml")
	git(t, root, "commit", "-q", "-m", "chore: approvers")
}

// signIntent asks the server for the intent of action and signs it as the Mac app would.
func (e *env) signIntent(path, action string, k *ecdsa.PrivateKey) string {
	e.t.Helper()
	var got struct {
		Intent  sign.Intent `json:"intent"`
		Message string      `json:"message"`
		Title   string      `json:"title"`
		Branch  string      `json:"branch"`
	}
	if code := e.json("POST", path+"/intent", `{"action":"`+action+`","approver":"mac"}`, asToken, &got); code != 200 {
		e.t.Fatalf("intent: %d", code)
	}
	if got.Title == "" || (action != sign.ActionApprovePlan) != (got.Branch != "") {
		e.t.Fatalf("Touch ID shows the title and, for push and PR, the branch: %+v", got)
	}
	msg, _ := base64.StdEncoding.DecodeString(got.Message)
	if string(msg) != string(got.Intent.Message()) {
		e.t.Fatal("the message to sign must be the intent's exact bytes")
	}
	h := sha256.Sum256(msg)
	sig, err := ecdsa.SignASN1(rand.Reader, k, h[:])
	if err != nil {
		e.t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"signedIntent": sign.Signed{Intent: got.Intent, Format: sign.FormatES256, Sig: base64.StdEncoding.EncodeToString(sig)}})
	return string(b)
}

func TestSignedApprovalWhereTheRepoListsApprovers(t *testing.T) {
	e := newEnv(t, false)
	k, pub := newKey(t)
	withApprovers(t, e.root, pub)
	path := e.taskPath("T-1")

	resp, b := e.req("POST", path+"/actions/approve-plan", `{"planSha256":"`+e.planSHA()+`"}`, asShell)
	if resp.StatusCode != 409 || !strings.Contains(string(b), codeSignatureRequired) || !strings.Contains(string(b), `"approvers":["mac"]`) || e.authCount() != 0 {
		t.Fatalf("an unsigned approval where approvers are listed: %d %s", resp.StatusCode, b)
	}
	if code := e.code("POST", path+"/intent", `{"action":"approve-plan","approver":"someone"}`, asToken); code != 400 {
		t.Fatalf("an unknown approver: %d", code)
	}

	other, _ := newKey(t)
	if code := e.act("T-1", "approve-plan", e.signIntent(path, sign.ActionApprovePlan, other), asToken, nil); code != 403 || e.state("T-1") != board.Planned {
		t.Fatalf("signed by a key that is not an approver's: %d", code)
	}
	signed := e.signIntent(path, sign.ActionApprovePlan, k)
	var res actResult
	if code := e.act("T-1", "approve-plan", signed, asToken, &res); code != 200 || !res.OK {
		t.Fatalf("signed approval: %d %+v", code, res)
	}
	if e.state("T-1") != board.Approved || e.authCount() != 0 {
		t.Fatal("the signature is the human factor: approved, and no OS dialog")
	}
	ws, _ := core.Open(e.root)
	tk, _ := ws.Store.Load("T-1")
	if res, _ := ws.Check(tk, board.Approved); !res.Pass {
		t.Fatalf("the gate must count the signed approval: %+v", res)
	}
	tk.State = board.Planned
	ws.Store.Save(tk)
	if code := e.act("T-1", "approve-plan", signed, asToken, nil); code != 403 {
		t.Fatalf("a replayed signature: %d", code)
	}
}

func TestSignedPush(t *testing.T) {
	e := newEnv(t, false)
	k, pub := newKey(t)
	withApprovers(t, e.root, pub)
	created := e.newWorktreeTask("Work")
	ws, _ := core.OpenWorktree(created.Worktree)
	os.WriteFile(ws.Store.PlanPath(created.ID), []byte(goodPlan), 0o644)
	path := e.taskPath(created.ID)
	if code := e.act(created.ID, "push", "", asShell, nil); code != 409 || e.authCount() != 0 || len(e.calls) != 0 {
		t.Fatalf("an unsigned push where approvers are listed: %d", code)
	}
	signed := e.signIntent(path, sign.ActionPush, k)
	var res actResult
	if code := e.act(created.ID, "push", signed, asToken, &res); code != 200 || !res.OK || len(e.calls) != 1 {
		t.Fatalf("signed push: %d %+v", code, res)
	}
	if code := e.act(created.ID, "push", signed, asToken, nil); code != 403 || len(e.calls) != 1 {
		t.Fatalf("the same signature pushes twice: %d", code)
	}
	if code := e.act(created.ID, "open-pr", signed, asToken, nil); code != 400 {
		t.Fatalf("a push intent used for a PR: %d", code)
	}
}

func TestRemoteSignedApprovalAndPR(t *testing.T) {
	re := newRemoteEnv(t)
	m := re.addMachine()
	peerRepo := repo(t)
	k, pub := newKey(t)
	withApprovers(t, peerRepo, pub)
	re.s.MacKey = &k.PublicKey
	git(t, peerRepo, "remote", "set-url", "origin", "git@github.com:acme/app.git")
	var p projectView
	if code := re.json("POST", "/api/projects", `{"path":"`+peerRepo+`","clientName":"Acme","machineId":"`+m.ID+`"}`, asToken, &p); code != 201 {
		t.Fatalf("remote project: %d", code)
	}
	path := "/api/projects/" + p.ID + "/tasks/T-1"
	resp, b := re.req("POST", path+"/actions/approve-plan", `{"planSha256":"x"}`, asShell)
	if resp.StatusCode != 409 || !strings.Contains(string(b), codeSignatureRequired) || re.authCount() != 1 {
		t.Fatalf("unsigned remote approval: %d %s", resp.StatusCode, b)
	}
	var res actResult
	if code := re.act2(path, "approve-plan", re.signIntent(path, sign.ActionApprovePlan, k), &res); code != 200 || !res.OK {
		t.Fatalf("signed remote approval: %d %+v", code, res)
	}
	ws, _ := core.Open(peerRepo)
	if tk, _ := ws.Store.Load("T-1"); tk.State != board.Approved {
		t.Fatalf("remote task: %s", tk.State)
	}

	// The machine has no gh: after verifying the signature it hands the PR to the Mac, which
	// checks with GitHub (here: a stand-in) that the branch is at the signed commit.
	git(t, peerRepo, "checkout", "-q", "-b", "th/T-1")
	head := strings.TrimSpace(git(t, peerRepo, "rev-parse", "HEAD"))
	run := re.s.Run
	re.s.Run = func(ctx context.Context, d string, env []string, name string, args ...string) ([]byte, error) {
		if name == "gh" && len(args) > 1 && args[0] == "api" {
			return []byte(head + "\n"), nil
		}
		return run(ctx, d, env, name, args...)
	}
	if code := re.act2(path, "open-pr", re.signIntent(path, sign.ActionOpenPR, k), &res); code != 200 || res.URL != "https://github.com/o/r/pull/7" {
		t.Fatalf("remote PR: %d %+v", code, res)
	}
	c := re.calls[len(re.calls)-1]
	if args := strings.Join(c.Args, " "); c.Name != "gh" || !strings.Contains(args, "--repo=github.com/acme/app") || !strings.Contains(args, "--head=th/T-1") {
		t.Fatalf("the Mac's gh call: %+v", c)
	}
}

func (re *remoteEnv) act2(path, action, body string, out any) int {
	re.t.Helper()
	return re.json("POST", path+"/actions/"+action, body, asToken, out)
}
