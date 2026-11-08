package serve

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spaghetticoders/truehand/internal/appstate"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/sign"
)

// configRepo commits a config with approver mac (key k) and base origin/main, with
// origin/main at that commit, then edits the config in the working tree.
func configRepo(t *testing.T, e *env) (*ecdsa.PrivateKey, string) {
	k, pub := newKey(t)
	indented := "      " + strings.ReplaceAll(strings.TrimSpace(pub), "\n", "\n      ")
	cfg := "version: 0\nbase: origin/main\napprovers:\n  - id: mac\n    name: Alice\n    public_key: |\n" + indented + "\ngates:\n  approved:\n    - check: plan_valid\n    - check: human_approval\n"
	os.WriteFile(filepath.Join(e.root, ".truehand/config.yaml"), []byte(cfg), 0o644)
	git(t, e.root, "add", ".truehand/config.yaml")
	git(t, e.root, "commit", "-q", "-m", "chore: approvers")
	git(t, e.root, "update-ref", "refs/remotes/origin/main", "HEAD")
	edited := cfg + "  verified: []\n"
	os.WriteFile(filepath.Join(e.root, ".truehand/config.yaml"), []byte(edited), 0o644)
	h := sha256.Sum256([]byte(edited))
	return k, hexs(h[:])
}

func hexs(b []byte) string {
	const d = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, d[c>>4], d[c&15])
	}
	return string(out)
}

func (e *env) signConfig(action, sha string, k *ecdsa.PrivateKey) (sign.Signed, int) {
	e.t.Helper()
	var got struct {
		Intent  sign.Intent `json:"intent"`
		Message string      `json:"message"`
	}
	body, _ := json.Marshal(map[string]string{"action": action, "approver": "mac", "configSha256": sha})
	if code := e.json("POST", "/api/projects/"+e.pid+"/config-intent", string(body), asToken, &got); code != 200 {
		return sign.Signed{}, code
	}
	msg, _ := base64.StdEncoding.DecodeString(got.Message)
	if string(msg) != string(got.Intent.Message()) {
		e.t.Fatal("message is not the intent's bytes")
	}
	h := sha256.Sum256(msg)
	sig, _ := ecdsa.SignASN1(rand.Reader, k, h[:])
	return sign.Signed{Intent: got.Intent, Format: sign.FormatES256, Sig: base64.StdEncoding.EncodeToString(sig)}, 200
}

var remoteTip string

func TestSignedCommitConfigAndPushBase(t *testing.T) {
	e := newEnv(t, false)
	var pushes [][]string
	e.s.Run = func(ctx context.Context, d string, env []string, name string, args ...string) ([]byte, error) {
		if name == "git" && len(args) > 0 && args[0] == "push" {
			e.mu.Lock()
			pushes = append(pushes, args)
			e.mu.Unlock()
			return []byte("pushed"), nil
		}
		if name == "git" && len(args) > 0 && args[0] == "ls-remote" {
			// origin's main is what origin/main was set to by configRepo.
			return []byte(remoteTip + "\trefs/heads/main\n"), nil
		}
		return runCmd(ctx, d, env, name, args...)
	}
	k, sha := configRepo(t, e)
	remoteTip = strings.TrimSpace(git(t, e.root, "rev-parse", "origin/main"))
	other, _ := newKey(t)

	// An unknown approver gets no intent; a signature by another key is refused.
	if code := e.code("POST", "/api/projects/"+e.pid+"/config-intent", `{"action":"commit-config","approver":"x","configSha256":"`+sha+`"}`, asToken); code != 400 {
		t.Fatalf("unknown approver = %d", code)
	}
	bad, _ := e.signConfig(sign.ActionCommitConfig, sha, other)
	b, _ := json.Marshal(map[string]any{"configSha256": sha, "signedIntent": bad})
	if code := e.code("POST", "/api/projects/"+e.pid+"/commit-config", string(b), asToken); code != 403 {
		t.Fatalf("commit-config signed by another key = %d", code)
	}
	good, _ := e.signConfig(sign.ActionCommitConfig, sha, k)
	b, _ = json.Marshal(map[string]any{"configSha256": sha, "signedIntent": good})
	var res actResult
	if code := e.json("POST", "/api/projects/"+e.pid+"/commit-config", string(b), asToken, &res); code != 200 || !res.OK {
		t.Fatalf("signed commit-config = %d %+v", code, res)
	}
	if e.authCount() != 0 {
		t.Fatal("the OS was asked for a signed commit")
	}
	if out := git(t, e.root, "log", "-1", "--format=%s"); !strings.Contains(out, "truehand config") {
		t.Fatalf("last commit: %s", out)
	}

	// push-base: from origin/main (lists mac) to the new commit.
	head := strings.TrimSpace(git(t, e.root, "rev-parse", "main"))
	pb, code := e.signConfig(sign.ActionPushBase, "", k)
	if code != 200 || pb.Intent.Head != head {
		t.Fatalf("push-base intent = %d %+v", code, pb.Intent)
	}
	var prev struct {
		Push pushView `json:"push"`
	}
	e.json("POST", "/api/projects/"+e.pid+"/config-intent", `{"action":"push-base","approver":"mac"}`, asToken, &prev)
	if prev.Push.From != remoteTip || prev.Push.Head != head || len(prev.Push.Commits) != 1 || !prev.Push.OnlyConfig || prev.Push.Commits[0].Files[0] != ".truehand/config.yaml" || !strings.Contains(prev.Push.Commits[0].Subject, "truehand config") {
		t.Fatalf("push preview = %+v", prev.Push)
	}
	b, _ = json.Marshal(map[string]any{"signedIntent": pb})
	if code := e.json("POST", "/api/projects/"+e.pid+"/push-base", string(b), asToken, &res); code != 200 || !res.OK {
		t.Fatalf("push-base = %d %+v", code, res)
	}
	if len(pushes) != 1 || strings.Join(pushes[0], " ") != "push --force-with-lease=refs/heads/main:"+remoteTip+" origin "+head+":refs/heads/main" {
		t.Fatalf("pushes = %v", pushes)
	}
	// The same signature again, even with the repo's own nonce record gone (H13).
	os.Remove(filepath.Join(e.root, ".git", "truehand", "intents.jsonl"))
	if code := e.code("POST", "/api/projects/"+e.pid+"/push-base", string(b), asToken); code != 403 {
		t.Fatalf("push-base replayed = %d", code)
	}
	// Unsigned, not even Touch ID: origin's base lists approvers (D3).
	for _, c := range []cred{asToken, asShell} {
		if code := e.code("POST", "/api/projects/"+e.pid+"/push-base", "", c); code != 409 {
			t.Fatalf("unsigned push-base = %d", code)
		}
	}
	if len(pushes) != 1 || e.authCount() != 0 {
		t.Fatalf("pushed again: %v (auths %d)", pushes, e.authCount())
	}
}

// Before the base lists any approver, enrolling the first one is a human action.
func TestFirstApproversNeedOnlyTheHuman(t *testing.T) {
	e := newEnv(t, false)
	os.WriteFile(filepath.Join(e.root, ".truehand/config.yaml"), []byte("version: 0\nbase: origin/main\ngates:\n  approved:\n    - check: plan_valid\n    - check: human_approval\n"), 0o644)
	git(t, e.root, "commit", "-q", "-am", "chore: remote base")
	tip := strings.TrimSpace(git(t, e.root, "rev-parse", "HEAD"))
	git(t, e.root, "update-ref", "refs/remotes/origin/main", tip)
	_, pub := newKey(t)
	indented := "      " + strings.ReplaceAll(strings.TrimSpace(pub), "\n", "\n      ")
	os.WriteFile(filepath.Join(e.root, ".truehand/config.yaml"), []byte("version: 0\nbase: origin/main\napprovers:\n  - id: mac\n    name: Alice\n    public_key: |\n"+indented+"\ngates:\n  approved:\n    - check: plan_valid\n    - check: human_approval\n"), 0o644)
	git(t, e.root, "commit", "-q", "-am", "chore(truehand): add truehand config")
	pushes := 0
	e.s.Run = func(ctx context.Context, d string, env []string, name string, args ...string) ([]byte, error) {
		switch {
		case name == "git" && args[0] == "push":
			pushes++
			return nil, nil
		case name == "git" && args[0] == "ls-remote":
			return []byte(tip + "\trefs/heads/main\n"), nil
		}
		return runCmd(ctx, d, env, name, args...)
	}
	var prev struct {
		Push      pushView `json:"push"`
		Approvers []string `json:"approvers"`
	}
	if c := e.json("GET", "/api/projects/"+e.pid+"/push-base", "", asToken, &prev); c != 200 || !prev.Push.OnlyConfig || len(prev.Push.Commits) != 1 || len(prev.Approvers) != 0 {
		t.Fatalf("preview = %d %+v", c, prev)
	}
	head := `{"from":"` + prev.Push.From + `","head":"` + prev.Push.Head + `"}`
	if code := e.code("POST", "/api/projects/"+e.pid+"/push-base", head, asToken); code == 200 || pushes != 0 {
		t.Fatalf("push-base from the token = %d", code)
	}
	// Confirmed for another head (or none): refused before anyone is asked.
	for _, b := range []string{"", `{"head":"` + prev.Push.Head + `"}`, `{"from":"` + prev.Push.From + `","head":"` + tip + `"}`} {
		if code := e.code("POST", "/api/projects/"+e.pid+"/push-base", b, asShell); code != 409 || pushes != 0 || e.authCount() != 0 {
			t.Fatalf("push-base for another head %q = %d", b, code)
		}
	}
	if code := e.code("POST", "/api/projects/"+e.pid+"/push-base", head, asShell); code != 200 || pushes != 1 || e.authCount() != 1 {
		t.Fatalf("push-base with Touch ID = %d (pushes %d, auths %d)", code, pushes, e.authCount())
	}
	if want := "invia le regole di demo (main): " + prev.Push.From[:7] + " → " + prev.Push.Head[:7] + ", solo .truehand/config.yaml."; !strings.HasSuffix(e.auths[0], want) {
		t.Fatalf("dialog = %q, want …%q", e.auths[0], want)
	}
	e.s.Store.UpdateState(func(st *appstate.State) error { st.Settings.Lang = "en"; return nil })
	if got := e.s.pushBasePrompt("demo", "main", prev.Push.From, prev.Push.Head); !strings.Contains(got, "push the rules of demo (main): "+prev.Push.From[:7]+" → "+prev.Push.Head[:7]+", only .truehand/config.yaml") {
		t.Fatalf("english dialog = %q", got)
	}
	// A commit beyond the config: refused on the human path too.
	os.WriteFile(filepath.Join(e.root, "README.md"), []byte("x"), 0o644)
	git(t, e.root, "add", "README.md")
	git(t, e.root, "commit", "-q", "-m", "docs: readme")
	e.json("GET", "/api/projects/"+e.pid+"/push-base", "", asToken, &prev)
	if prev.Push.OnlyConfig {
		t.Fatal("preview says only config with a README commit")
	}
	if code := e.code("POST", "/api/projects/"+e.pid+"/push-base", `{"from":"`+prev.Push.From+`","head":"`+prev.Push.Head+`"}`, asShell); code != 409 || pushes != 1 {
		t.Fatalf("push-base with a README commit = %d", code)
	}
}

func TestSignedPushNonceSurvivesTheRepo(t *testing.T) {
	e := newEnv(t, false)
	k, pub := newKey(t)
	withApprovers(t, e.root, pub)
	created := e.newWorktreeTask("Work")
	ws, _ := core.OpenWorktree(created.Worktree)
	os.WriteFile(ws.Store.PlanPath(created.ID), []byte(goodPlan), 0o644)
	signed := e.signIntent(e.taskPath(created.ID), sign.ActionPush, k)
	if code := e.act(created.ID, "push", signed, asToken, nil); code != 200 || len(e.calls) != 1 {
		t.Fatalf("signed push = %d", code)
	}
	// An agent deletes the repo's record of spent nonces: the server still knows (H13).
	gd := strings.TrimSpace(git(t, created.Worktree, "rev-parse", "--git-dir"))
	if !filepath.IsAbs(gd) {
		gd = filepath.Join(created.Worktree, gd)
	}
	if err := os.Remove(filepath.Join(gd, "truehand", "intents.jsonl")); err != nil {
		t.Fatal(err)
	}
	if code := e.act(created.ID, "push", signed, asToken, nil); code != 403 || len(e.calls) != 1 {
		t.Fatalf("replay after the repo forgot = %d (calls %d)", code, len(e.calls))
	}
}

// The approvers of a push-base are read at origin's real tip: an agent that rewinds the
// local origin/main to a commit where a revoked key was an approver gains nothing.
func TestPushBaseReadsTheRemoteTip(t *testing.T) {
	e := newEnv(t, false)
	revoked, _ := configRepo(t, e)
	old := strings.TrimSpace(git(t, e.root, "rev-parse", "origin/main"))
	// The base moves on: mac now has a new key, and origin has that commit.
	k, pub := newKey(t)
	indented := "      " + strings.ReplaceAll(strings.TrimSpace(pub), "\n", "\n      ")
	cfg := "version: 0\nbase: origin/main\napprovers:\n  - id: mac\n    name: Alice\n    public_key: |\n" + indented + "\ngates:\n  approved:\n    - check: plan_valid\n    - check: human_approval\n"
	os.WriteFile(filepath.Join(e.root, ".truehand/config.yaml"), []byte(cfg), 0o644)
	git(t, e.root, "commit", "-q", "-am", "chore: rotate key")
	tip := strings.TrimSpace(git(t, e.root, "rev-parse", "HEAD"))
	os.WriteFile(filepath.Join(e.root, ".truehand/config.yaml"), []byte(cfg+"  verified: []\n"), 0o644)
	git(t, e.root, "commit", "-q", "-am", "chore(truehand): add truehand config")
	var pushes int
	e.s.Run = func(ctx context.Context, d string, env []string, name string, args ...string) ([]byte, error) {
		switch {
		case name == "git" && args[0] == "push":
			pushes++
			return nil, nil
		case name == "git" && args[0] == "ls-remote":
			return []byte(tip + "\trefs/heads/main\n"), nil
		}
		return runCmd(ctx, d, env, name, args...)
	}
	// The agent's rewind.
	git(t, e.root, "update-ref", "refs/remotes/origin/main", old)
	sg, code := e.signConfig(sign.ActionPushBase, "", revoked)
	if code == 200 {
		b, _ := json.Marshal(map[string]any{"signedIntent": sg})
		if c := e.code("POST", "/api/projects/"+e.pid+"/push-base", string(b), asToken); c != 403 || pushes != 0 {
			t.Fatalf("push-base signed by the revoked key = %d (pushes %d)", c, pushes)
		}
	}
	sg, _ = e.signConfig(sign.ActionPushBase, "", k)
	b, _ := json.Marshal(map[string]any{"signedIntent": sg})
	if c := e.code("POST", "/api/projects/"+e.pid+"/push-base", string(b), asToken); c != 200 || pushes != 1 {
		t.Fatalf("push-base signed by the current key = %d (pushes %d)", c, pushes)
	}
}

func TestPushFailureNamesTheHookBinary(t *testing.T) {
	e := newEnv(t, false)
	e.s.TruehandBin = "/opt/truehand/bin"
	created := e.newWorktreeTask("Work")
	ws, _ := core.OpenWorktree(created.Worktree)
	os.WriteFile(ws.Store.PlanPath(created.ID), []byte(goodPlan), 0o644)
	e.s.Run = func(ctx context.Context, d string, env []string, name string, args ...string) ([]byte, error) {
		if name == "git" && args[0] == "push" {
			return []byte("old truehand: field approvers not found"), errString("exit 1")
		}
		return runCmd(ctx, d, env, name, args...)
	}
	var res actResult
	e.act(created.ID, "push", "", asShell, &res)
	if !strings.Contains(res.Error, "/opt/truehand/bin/truehand") || !strings.Contains(res.Output, "field approvers") {
		t.Fatalf("push failure = %+v", res)
	}
}

// An owner's passkey confirms one push: signed for A, it does not push B.
func TestOwnerPushBaseIsBoundToThePushShown(t *testing.T) {
	e := newEnv(t, false)
	a := e.headless()
	s25 := e.phone(false)
	e.bootstrap(a, s25)
	os.WriteFile(filepath.Join(e.root, ".truehand/config.yaml"), []byte("version: 0\nbase: origin/main\ngates:\n  approved:\n    - check: plan_valid\n    - check: human_approval\n"), 0o644)
	git(t, e.root, "commit", "-q", "-am", "chore: remote base")
	tip := strings.TrimSpace(git(t, e.root, "rev-parse", "HEAD"))
	git(t, e.root, "update-ref", "refs/remotes/origin/main", tip)
	cfgA := "version: 0\nbase: origin/main\ngates:\n  approved:\n    - check: plan_valid\n    - check: human_approval\n  verified: []\n"
	os.WriteFile(filepath.Join(e.root, ".truehand/config.yaml"), []byte(cfgA), 0o644)
	git(t, e.root, "commit", "-q", "-am", "chore(truehand): add truehand config")
	var pushed []string
	e.s.Run = func(ctx context.Context, d string, env []string, name string, args ...string) ([]byte, error) {
		switch {
		case name == "git" && args[0] == "push":
			pushed = append(pushed, args[len(args)-1])
			return nil, nil
		case name == "git" && args[0] == "ls-remote":
			return []byte(tip + "\trefs/heads/main\n"), nil
		}
		return runCmd(ctx, d, env, name, args...)
	}
	var prev struct {
		Push pushView `json:"push"`
	}
	s25.json("GET", "/api/projects/"+e.pid+"/push-base", "", nil, &prev)
	body := `{"from":"` + prev.Push.From + `","head":"` + prev.Push.Head + `"}`
	path := "/api/projects/" + e.pid + "/push-base"
	_, h := s25.signed(appstate.ActionOwner, map[string]string{"method": "POST", "path": path, "bodySha256": bodySHA(body)})
	// Meanwhile the branch moves to B (another config commit).
	os.WriteFile(filepath.Join(e.root, ".truehand/config.yaml"), []byte(cfgA+"  reviewed: []\n"), 0o644)
	git(t, e.root, "commit", "-q", "-am", "chore(truehand): more")
	if c := s25.json("POST", path, body, map[string]string{ownerHeader: h}, nil); c != 409 || len(pushed) != 0 {
		t.Fatalf("push A confirmed, branch at B = %d (pushed %v)", c, pushed)
	}
	// The signature for A over a body naming B: refused.
	headB := strings.TrimSpace(git(t, e.root, "rev-parse", "main"))
	_, h = s25.signed(appstate.ActionOwner, map[string]string{"method": "POST", "path": path, "bodySha256": bodySHA(body)})
	bodyB := `{"from":"` + prev.Push.From + `","head":"` + headB + `"}`
	if c := s25.json("POST", path, bodyB, map[string]string{ownerHeader: h}, nil); c != 403 || len(pushed) != 0 {
		t.Fatalf("signature for A on a body for B = %d", c)
	}
	// Signed for B, it pushes B.
	_, h = s25.signed(appstate.ActionOwner, map[string]string{"method": "POST", "path": path, "bodySha256": bodySHA(bodyB)})
	if c := s25.json("POST", path, bodyB, map[string]string{ownerHeader: h}, nil); c != 200 || len(pushed) != 1 || pushed[0] != headB+":refs/heads/main" {
		t.Fatalf("push B = %d %v", c, pushed)
	}
	// config-intent shows the push even without approvers.
	var ci struct {
		Code string   `json:"code"`
		Push pushView `json:"push"`
	}
	s25.json("POST", "/api/projects/"+e.pid+"/config-intent", `{"action":"push-base","approver":"x"}`, nil, &ci)
	if ci.Code != codeNoApprovers || ci.Push.Head != headB || ci.Push.From != tip || !ci.Push.OnlyConfig {
		t.Fatalf("config-intent without approvers = %+v", ci)
	}
}

func TestGitEnvHasNoGraftHint(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "x")
	out, err := runCmd(context.Background(), dir, gitEnv(""), "git", "merge-base", "HEAD", "nope")
	if err == nil || strings.Contains(strings.ToLower(string(out)), "graft") {
		t.Fatalf("git error mentions grafts: %v %s", err, out)
	}
}

func TestGitEnvKeepsInheritedConfig(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "user.name")
	t.Setenv("GIT_CONFIG_VALUE_0", "Inherited")
	out, err := runCmd(context.Background(), t.TempDir(), gitEnv(""), "git", "config", "--get", "user.name")
	if err != nil || strings.TrimSpace(string(out)) != "Inherited" {
		t.Fatalf("inherited config lost: %v %q", err, out)
	}
	out, _ = runCmd(context.Background(), t.TempDir(), gitEnv(""), "git", "config", "--get", "advice.graftFileDeprecated")
	if strings.TrimSpace(string(out)) != "false" {
		t.Fatalf("advice = %q", out)
	}
}

func TestPushBaseBehindTheRemote(t *testing.T) {
	e := newEnv(t, false)
	configRepo(t, e)
	e.s.Run = func(ctx context.Context, d string, env []string, name string, args ...string) ([]byte, error) {
		if name == "git" && args[0] == "ls-remote" {
			return []byte(strings.Repeat("e", 40) + "\trefs/heads/main\n"), nil
		}
		return runCmd(ctx, d, env, name, args...)
	}
	var out struct {
		Code string `json:"code"`
	}
	if c := e.json("GET", "/api/projects/"+e.pid+"/push-base", "", asToken, &out); c != 409 || out.Code != "base-behind" {
		t.Fatalf("preview with origin ahead = %d %q", c, out.Code)
	}
}

// The human path checks what the signed one does: a config at head that does not load, or a
// merge (even of the config alone), is not pushed.
func TestOwnerPushBaseChecksTheConfig(t *testing.T) {
	e := newEnv(t, false)
	a := e.headless()
	s25 := e.phone(false)
	e.bootstrap(a, s25)
	base := "version: 0\nbase: origin/main\ngates:\n  approved:\n    - check: plan_valid\n    - check: human_approval\n"
	os.WriteFile(filepath.Join(e.root, ".truehand/config.yaml"), []byte(base), 0o644)
	git(t, e.root, "commit", "-q", "-am", "chore: remote base")
	tip := strings.TrimSpace(git(t, e.root, "rev-parse", "HEAD"))
	git(t, e.root, "update-ref", "refs/remotes/origin/main", tip)
	pushed := 0
	e.s.Run = func(ctx context.Context, d string, env []string, name string, args ...string) ([]byte, error) {
		switch {
		case name == "git" && args[0] == "push":
			pushed++
			return nil, nil
		case name == "git" && args[0] == "ls-remote":
			return []byte(tip + "\trefs/heads/main\n"), nil
		}
		return runCmd(ctx, d, env, name, args...)
	}
	path := "/api/projects/" + e.pid + "/push-base"
	try := func() (int, string) {
		var prev struct {
			Push pushView `json:"push"`
		}
		s25.json("GET", path, "", nil, &prev)
		body := `{"from":"` + prev.Push.From + `","head":"` + prev.Push.Head + `"}`
		var out struct {
			Code string `json:"code"`
		}
		c := s25.asOwner("POST", path, body, &out)
		return c, out.Code
	}
	// A hand-made commit that breaks the config.
	os.WriteFile(filepath.Join(e.root, ".truehand/config.yaml"), []byte(base+"gates: [oops\n"), 0o644)
	git(t, e.root, "commit", "-q", "-am", "chore: broken config")
	if c, code := try(); c != 409 || code != "config-invalid" || pushed != 0 {
		t.Fatalf("broken config = %d %q (pushed %d)", c, code, pushed)
	}
	// Committed broken, fixed only in the working tree: the commit pushed is still broken.
	os.WriteFile(filepath.Join(e.root, ".truehand/config.yaml"), []byte(base), 0o644)
	if c, code := try(); c != 409 || code != "config-invalid" || pushed != 0 {
		t.Fatalf("broken config at head, good working tree = %d %q (pushed %d)", c, code, pushed)
	}
	// A merge that changes only the config.
	git(t, e.root, "reset", "-q", "--hard", tip)
	git(t, e.root, "checkout", "-q", "-b", "side")
	os.WriteFile(filepath.Join(e.root, ".truehand/config.yaml"), []byte(base+"  verified: []\n"), 0o644)
	git(t, e.root, "commit", "-q", "-am", "chore: side config")
	git(t, e.root, "checkout", "-q", "main")
	git(t, e.root, "commit", "-q", "--allow-empty", "-m", "chore: empty")
	git(t, e.root, "merge", "-q", "--no-ff", "-m", "merge config", "side")
	if c, code := try(); c != 409 || code != "not-only-config" || pushed != 0 {
		t.Fatalf("merge of the config = %d %q (pushed %d)", c, code, pushed)
	}
	if prev := pushPreview(e.ws(t).Repo, tip, strings.TrimSpace(git(t, e.root, "rev-parse", "HEAD"))); prev.OnlyConfig {
		t.Fatal("the preview calls a merge only config")
	}
}

func (e *env) ws(t *testing.T) *core.Workspace {
	ws, err := core.Open(e.root)
	if err != nil {
		t.Fatal(err)
	}
	return ws
}
