package serve

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/spaghetticoders/truehand/internal/agents"
	"github.com/spaghetticoders/truehand/internal/appstate"
	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/evidence"
)

const goodPlan = "# T\n\n## Goal\ng\n\n## Scope\ns\n\n## Out of scope\no\n\n## Steps\n1. a — verify: b\n\n## Verification\nv\n\n## Risks\nr\n"

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// repo is a project with a committed config and task T-1, planned with a complete plan.
func repo(t *testing.T) string {
	dir := filepath.Join(t.TempDir(), "demo")
	os.MkdirAll(dir, 0o755)
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "config", "user.email", "h@x.y")
	git(t, dir, "config", "user.name", "H")
	os.MkdirAll(filepath.Join(dir, ".truehand"), 0o755)
	os.WriteFile(filepath.Join(dir, ".truehand/config.yaml"), []byte("version: 0\nbase: main\ngates:\n  approved:\n    - check: plan_valid\n    - check: human_approval\n"), 0o644)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "chore: init")
	git(t, dir, "remote", "add", "origin", "git@github.com:acme/demo.git")
	ws, err := core.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.NewTask("T-1", "Demo", ""); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(ws.Store.PlanPath("T-1"), []byte(goodPlan), 0o644)
	os.MkdirAll(filepath.Join(ws.Store.TaskDir("T-1"), "evidence"), 0o755)
	os.WriteFile(filepath.Join(ws.Store.TaskDir("T-1"), "evidence", "x.png"), []byte("png"), 0o644)
	return dir
}

const (
	testToken = "token-0123456789abcdef0123456789abcdef"
	testShell = "shell-0123456789abcdef0123456789abcdef"
)

const fakeClaude = `#!/bin/sh
echo "ARGS:$*"
while IFS= read -r line; do
  case "$line" in
    quit) exit 0 ;;
    *) echo "GOT:$line" ;;
  esac
done
`

type call struct {
	Dir  string
	Env  []string
	Name string
	Args []string
}

type env struct {
	t      *testing.T
	s      *Server
	srv    *httptest.Server
	pid    string
	root   string
	mu     sync.Mutex
	auths  []string
	authOK bool
	calls  []call
}

func newEnv(t *testing.T, readOnly bool) *env {
	root := repo(t)
	dir := t.TempDir()
	store, err := appstate.Open(filepath.Join(dir, "app"))
	if err != nil {
		t.Fatal(err)
	}
	store.SetToken(testToken)
	p, err := store.AddProject(root, "Acme", "")
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, pid: p.ID, root: root, authOK: true}
	e.s = &Server{
		Store: store, ReadOnly: readOnly, ShellSecretHash: appstate.HashSecret(testShell),
		TruehandBin: "/opt/truehand/bin", WorktreesDir: filepath.Join(dir, "worktrees"), Home: dir, Version: "test",
		HumanAuth: func(reason string) error {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.auths = append(e.auths, reason)
			if !e.authOK {
				return errCancelled
			}
			return nil
		},
		Run: func(ctx context.Context, d string, env []string, name string, args ...string) ([]byte, error) {
			e.mu.Lock()
			e.calls = append(e.calls, call{d, env, name, args})
			e.mu.Unlock()
			if name == "gh" && len(args) > 1 && args[1] == "create" {
				return []byte("Creating pull request\nhttps://github.com/o/r/pull/7\n"), nil
			}
			return []byte("ok"), nil
		},
		LookPath: func(name string) (string, error) {
			if name == "gh" {
				return "/usr/local/bin/gh", nil
			}
			return "", os.ErrNotExist
		},
	}
	if !readOnly {
		bin := filepath.Join(dir, "claude")
		os.WriteFile(bin, []byte(fakeClaude), 0o755)
		m, err := agents.NewManager(agents.Config{
			Store: store, Claude: bin, PluginDir: "/p/plugin", Settings: "/p/settings.json", HookAddr: "127.0.0.1:1",
			AllowCwd: func(string) bool { return true }, OnChange: e.s.SessionChanged, KillGrace: time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		e.s.Agents = m
	}
	// Tool probes would run through e.s.Run and show up in e.calls: start with fresh results.
	e.s.health = &healthCache{at: time.Now().Add(24 * time.Hour)}
	e.srv = httptest.NewServer(e.s.Handler())
	t.Cleanup(func() {
		e.srv.Close()
		if e.s.Agents != nil {
			list, _ := e.s.Agents.Sessions()
			for _, x := range list {
				if appstate.Live(x.Status) {
					e.s.Agents.Delete(x.ID)
				}
			}
		}
	})
	return e
}

type errString string

func (e errString) Error() string { return string(e) }

const errCancelled = errString("authentication cancelled")

func (e *env) authCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.auths)
}

// cred is how a request authenticates.
type cred struct {
	token  bool
	shell  bool
	cookie string
	origin string // "" = same as host for cookie requests, "-" = none
	host   string
}

var (
	asShell  = cred{token: true, shell: true}
	asToken  = cred{token: true}
	anon     = cred{}
	jsonType = "application/json"
)

func (e *env) req(method, path, body string, c cred) (*http.Response, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if body != "" {
		req.Header.Set("Content-Type", jsonType)
	}
	if c.token {
		req.Header.Set("Authorization", "Bearer "+testToken)
	}
	if c.shell {
		req.Header.Set(shellHeader, testShell)
	}
	if c.cookie != "" {
		req.AddCookie(&http.Cookie{Name: cookieName, Value: c.cookie})
	}
	if c.host != "" {
		req.Host = c.host
	}
	switch c.origin {
	case "":
		if c.cookie != "" {
			req.Header.Set("Origin", "http://"+req.Host)
			if req.Host == "" {
				req.Header.Set("Origin", e.srv.URL)
			}
		}
	case "-":
	default:
		req.Header.Set("Origin", c.origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func (e *env) code(method, path, body string, c cred) int {
	e.t.Helper()
	resp, _ := e.req(method, path, body, c)
	return resp.StatusCode
}

func (e *env) json(method, path, body string, c cred, out any) int {
	e.t.Helper()
	resp, b := e.req(method, path, body, c)
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			e.t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, b)
		}
	}
	return resp.StatusCode
}

// pairDevice pairs a phone and returns its cookie.
func (e *env) pairDevice() string {
	e.t.Helper()
	var pc struct{ Code string }
	if code := e.json("POST", "/api/pair/code", "", asShell, &pc); code != 200 {
		e.t.Fatalf("pair code: %d", code)
	}
	resp, b := e.req("POST", "/api/pair", `{"code":"`+pc.Code+`","deviceName":"S25"}`, anon)
	if resp.StatusCode != 200 {
		e.t.Fatalf("pair: %d %s", resp.StatusCode, b)
	}
	for _, c := range resp.Cookies() {
		if c.Name == cookieName {
			return c.Value
		}
	}
	e.t.Fatal("no cookie")
	return ""
}

func (e *env) taskPath(id string) string { return "/api/projects/" + e.pid + "/tasks/" + id }

func (e *env) planSHA() string {
	var d struct {
		PlanSHA256 string `json:"planSha256"`
	}
	e.json("GET", e.taskPath("T-1"), "", asToken, &d)
	return d.PlanSHA256
}

func (e *env) act(id, action, body string, c cred, out any) int {
	e.t.Helper()
	return e.json("POST", e.taskPath(id)+"/actions/"+action, body, c, out)
}

func (e *env) snapshot() snapshotView {
	e.t.Helper()
	var snap snapshotView
	if code := e.json("GET", "/api/snapshot", "", asToken, &snap); code != 200 {
		e.t.Fatalf("snapshot: %d", code)
	}
	return snap
}

func (e *env) summary(id string) taskSummary {
	for _, t := range e.snapshot().Tasks {
		if t.ID == id {
			return t
		}
	}
	e.t.Fatalf("no task %s in the snapshot", id)
	return taskSummary{}
}

// newWorktreeTask creates a task in its own worktree through the API.
func (e *env) newWorktreeTask(title string) taskSummary {
	e.t.Helper()
	var t taskSummary
	if code := e.json("POST", "/api/projects/"+e.pid+"/tasks", `{"title":"`+title+`"}`, asToken, &t); code != 201 {
		e.t.Fatalf("create: %d", code)
	}
	return t
}

func (e *env) state(id string) board.State {
	ws, _ := core.Open(e.root)
	t, _ := ws.Store.Load(id)
	return t.State
}

func TestHostAllowlist(t *testing.T) {
	e := newEnv(t, false)
	if code := e.code("GET", "/api/me", "", cred{token: true, host: "evil.example:80"}); code != 403 {
		t.Fatalf("foreign Host: %d", code)
	}
	if code := e.code("GET", "/", "", cred{host: "rebind.attacker.test"}); code != 403 {
		t.Fatalf("foreign Host on the app page: %d", code)
	}
	if code := e.code("GET", "/api/me", "", cred{token: true, host: "100.101.102.103:7878"}); code != 403 {
		t.Fatalf("tailnet IP not configured: %d", code)
	}
	e.s.Hosts = []string{"100.101.102.103"}
	if code := e.code("GET", "/api/me", "", cred{token: true, host: "100.101.102.103:7878"}); code != 200 {
		t.Fatalf("configured tailnet IP: %d", code)
	}
	if code := e.code("GET", "/api/me", "", cred{token: true, host: "localhost:7878"}); code != 200 {
		t.Fatalf("localhost: %d", code)
	}
}

func TestOriginMustBeTheHost(t *testing.T) {
	e := newEnv(t, false)
	body := `{"body":"hi"}`
	path := e.taskPath("T-1") + "/comments"
	if code := e.code("POST", path, body, cred{token: true, origin: "http://evil.example"}); code != 403 {
		t.Fatalf("cross-origin POST with the token: %d", code)
	}
	cookie := e.pairDevice()
	if code := e.code("POST", path, body, cred{cookie: cookie, origin: "http://evil.example"}); code != 403 {
		t.Fatalf("cross-origin POST with a cookie: %d", code)
	}
	if code := e.code("POST", path, body, cred{cookie: cookie, origin: "-"}); code != 403 {
		t.Fatalf("cookie POST without Origin: %d", code)
	}
	if code := e.code("POST", path, body, cred{cookie: cookie}); code != 201 {
		t.Fatalf("same-origin cookie POST: %d", code)
	}
	if code := e.code("POST", path, body, asToken); code != 201 {
		t.Fatalf("token POST without Origin (the app's backend): %d", code)
	}
}

func TestCredentials(t *testing.T) {
	e := newEnv(t, false)
	if code := e.code("GET", "/api/me", "", anon); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
	if code := e.code("GET", "/api/snapshot?token="+testToken, "", anon); code != 401 {
		t.Fatalf("the token must never be accepted in the URL: %d", code)
	}
	if code := e.code("GET", "/api/me", "", cred{cookie: "made-up"}); code != 401 {
		t.Fatalf("unknown cookie: %d", code)
	}
	var me meView
	e.json("GET", "/api/me", "", asToken, &me)
	if me.CanApprove {
		t.Fatal("the token alone must not approve")
	}
	e.json("GET", "/api/me", "", asShell, &me)
	if !me.CanApprove {
		t.Fatal("token and shell secret")
	}
	_, b := e.req("GET", "/api/settings", "", asShell)
	if strings.Contains(string(b), testToken) || strings.Contains(strings.ToLower(string(b)), "token") {
		t.Fatalf("settings must never carry the token: %s", b)
	}
}

func TestPairing(t *testing.T) {
	e := newEnv(t, false)
	if code := e.code("POST", "/api/pair/code", "", asToken); code != 403 || e.authCount() != 0 {
		t.Fatalf("pairing code without the app's secret: %d, %d auth", code, e.authCount())
	}
	var pc struct{ Code string }
	e.json("POST", "/api/pair/code", "", asShell, &pc)
	if e.authCount() != 1 || len(pc.Code) != 8 {
		t.Fatalf("pairing code needs the OS authentication: %d %q", e.authCount(), pc.Code)
	}
	if code := e.code("POST", "/api/pair", `{"code":"WRONG123","deviceName":"x"}`, anon); code != 401 {
		t.Fatalf("wrong code: %d", code)
	}
	resp, _ := e.req("POST", "/api/pair", `{"code":"`+strings.ToLower(pc.Code[:4]+"-"+pc.Code[4:])+`","deviceName":"S25"}`, anon)
	if resp.StatusCode != 200 {
		t.Fatalf("pair: %d", resp.StatusCode)
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == cookieName {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" {
		t.Fatalf("device cookie must be HttpOnly, SameSite=Strict: %+v", cookie)
	}
	if code := e.code("POST", "/api/pair", `{"code":"`+pc.Code+`","deviceName":"again"}`, anon); code != 401 {
		t.Fatalf("a pairing code works once: %d", code)
	}
	var me meView
	e.json("GET", "/api/me", "", cred{cookie: cookie.Value}, &me)
	if me.Via != "device" || me.CanApprove || me.DeviceName != "S25" {
		t.Fatalf("me: %+v", me)
	}
	b, _ := os.ReadFile(filepath.Join(e.s.Store.Dir, "auth.json"))
	if strings.Contains(string(b), cookie.Value) || strings.Contains(string(b), testToken) {
		t.Fatal("auth.json must hold hashes only")
	}
	if devs := e.snapshot().Settings.Remote.Devices; len(devs) != 1 || devs[0].Name != "S25" {
		t.Fatalf("paired devices in settings: %+v", devs)
	}
	var devs []deviceView
	e.json("GET", "/api/devices", "", asToken, &devs)
	other := e.pairDevice()
	if code := e.code("DELETE", "/api/devices/"+devs[0].ID, "", cred{cookie: other}); code != 403 {
		t.Fatalf("a phone revoking another device: %d", code)
	}
	if code := e.code("DELETE", "/api/devices/"+devs[0].ID, "", asToken); code != 403 {
		t.Fatalf("the token alone revoking a device: %d", code)
	}
	if code := e.code("DELETE", "/api/devices/"+devs[0].ID, "", cred{cookie: cookie.Value}); code != 204 {
		t.Fatalf("a phone signing itself out: %d", code)
	}
	if code := e.code("GET", "/api/me", "", cred{cookie: cookie.Value}); code != 401 {
		t.Fatalf("revoked device: %d", code)
	}
	for i := 0; i < pairMaxFails; i++ {
		e.code("POST", "/api/pair", `{"code":"BAD00000","deviceName":"x"}`, anon)
	}
	if code := e.code("POST", "/api/pair", `{"code":"BAD00000","deviceName":"x"}`, anon); code != 429 {
		t.Fatalf("guessing codes must be throttled: %d", code)
	}
}

func TestLoginTurnsTheTokenIntoACookie(t *testing.T) {
	e := newEnv(t, false)
	resp, _ := e.req("POST", "/api/login", "", asToken)
	if resp.StatusCode != 204 || len(resp.Cookies()) != 1 {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	c := resp.Cookies()[0].Value
	var me meView
	e.json("GET", "/api/me", "", cred{cookie: c, shell: true}, &me)
	if !me.CanApprove {
		t.Fatal("the app's web view approves with its cookie plus the shell secret")
	}
	if code := e.code("POST", "/api/login", "", cred{cookie: c}); code != 403 {
		t.Fatalf("login with a cookie: %d", code)
	}
}

// The human-only actions: never with the token alone, never the OS dialog without the
// app's secret, and from a phone they wait in a queue for the Mac.
func TestApproveNeedsAPersonAtTheMac(t *testing.T) {
	e := newEnv(t, false)
	sha := e.planSHA()
	body := `{"planSha256":"` + sha + `"}`
	approvals := func() int {
		ws, _ := core.Open(e.root)
		a, _ := ws.Store.Approvals("T-1")
		return len(a)
	}
	if s := e.summary("T-1"); s.WaitReason != waitApprovePlan || s.PrimaryAction != actApprovePlan || !s.PlanReady {
		t.Fatalf("a ready plan waits for approval: %+v", s)
	}
	if code := e.act("T-1", "approve-plan", body, anon, nil); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
	if code := e.act("T-1", "approve-plan", `{"planSha256":"stale"}`, asShell, nil); code != 409 || e.authCount() != 0 {
		t.Fatalf("stale plan must be refused before any dialog: %d, %d", code, e.authCount())
	}
	var q actResult
	if code := e.act("T-1", "approve-plan", body, asToken, &q); code != 202 || !q.Queued || q.QueuedID == "" {
		t.Fatalf("token alone must queue: %d %+v", code, q)
	}
	cookie := e.pairDevice()
	authsAfterPair := e.authCount()
	var q2 actResult
	if code := e.act("T-1", "approve-plan", body, cred{cookie: cookie}, &q2); code != 202 || q2.QueuedID != q.QueuedID {
		t.Fatalf("phone must queue (same request once): %d %+v", code, q2)
	}
	if e.authCount() != authsAfterPair || approvals() != 0 || e.state("T-1") != board.Planned {
		t.Fatal("a queued approval must neither show the OS dialog nor approve")
	}
	snap := e.snapshot()
	if len(snap.Queued) != 1 || snap.Queued[0].ID != q.QueuedID || snap.Queued[0].Kind != "approve" || snap.Queued[0].TaskID != "T-1" {
		t.Fatalf("the queued approval must be in the snapshot: %+v", snap.Queued)
	}
	confirm := "/api/queued/" + q.QueuedID + "/confirm"
	if code := e.code("POST", confirm, "", cred{cookie: cookie}); code != 403 || e.authCount() != authsAfterPair {
		t.Fatalf("a phone cannot confirm its own request: %d", code)
	}
	e.authOK = false
	if code := e.code("POST", confirm, "", asShell); code != 403 || approvals() != 0 {
		t.Fatalf("cancelled authentication: %d", code)
	}
	e.authOK = true
	var res actResult
	if code := e.json("POST", confirm, "", asShell, &res); code != 200 || !res.OK {
		t.Fatalf("confirm: %d %+v", code, res)
	}
	if approvals() != 1 || e.state("T-1") != board.Approved {
		t.Fatal("confirmation must approve and move to approved")
	}
	ws, _ := core.Open(e.root)
	a, _ := ws.Store.Approvals("T-1")
	if !strings.HasPrefix(a[0].Via, "app+os-auth") {
		t.Fatalf("via: %q", a[0].Via)
	}
	if left := e.snapshot().Queued; len(left) != 0 {
		t.Fatalf("queue not emptied: %v", left)
	}
	if s := e.summary("T-1"); s.WaitReason != "" || s.PrimaryAction != actStartAgent {
		t.Fatalf("approved: %+v", s)
	}
}

func TestApproveFromTheMac(t *testing.T) {
	e := newEnv(t, false)
	body := `{"planSha256":"` + e.planSHA() + `"}`
	e.authOK = false
	if code := e.act("T-1", "approve-plan", body, asShell, nil); code != 403 || e.state("T-1") != board.Planned {
		t.Fatalf("cancelled: %d", code)
	}
	e.authOK = true
	var res actResult
	if code := e.act("T-1", "approve-plan", body, asShell, &res); code != 200 || !res.OK {
		t.Fatalf("approve: %d %+v", code, res)
	}
	if !strings.Contains(e.auths[len(e.auths)-1], "T-1") {
		t.Fatalf("the dialog names the task: %q", e.auths[len(e.auths)-1])
	}
	if e.state("T-1") != board.Approved {
		t.Fatal("not moved")
	}
}

func TestPushAndPRNeedAPersonAtTheMac(t *testing.T) {
	e := newEnv(t, false)
	created := e.newWorktreeTask("Work")
	if code := e.act(created.ID, "push", "", asToken, nil); code != 202 {
		t.Fatalf("push with the token alone must queue: %d", code)
	}
	if code := e.act(created.ID, "open-pr", "", asToken, nil); code != 202 {
		t.Fatalf("PR with the token alone must queue: %d", code)
	}
	if len(e.calls) != 0 || e.authCount() != 0 {
		t.Fatalf("nothing may run before a person confirms: %v", e.calls)
	}
	if code := e.act("T-1", "push", "", asShell, nil); code != 400 {
		t.Fatalf("pushing the base branch must be refused: %d", code)
	}
	var out actResult
	if code := e.act(created.ID, "push", "", asShell, &out); code != 200 || !out.OK {
		t.Fatalf("push: %d %+v", code, out)
	}
	c := e.calls[len(e.calls)-1]
	if c.Name != "git" || strings.Join(c.Args, " ") != "push -u origin th/"+created.ID {
		t.Fatalf("push call: %+v", c)
	}
	path := ""
	for _, kv := range c.Env {
		if strings.HasPrefix(kv, "PATH=") {
			path = kv
		}
	}
	if !strings.HasPrefix(path, "PATH=/opt/truehand/bin:") {
		t.Fatalf("git push must find this truehand first on PATH for the pre-push hook: %q", path)
	}
	if c.Dir != created.Worktree {
		t.Fatalf("push runs in the task's worktree: %q", c.Dir)
	}
	if code := e.act(created.ID, "open-pr", "", asShell, &out); code != 200 || out.URL != "https://github.com/o/r/pull/7" {
		t.Fatalf("pr: %d %+v", code, out)
	}
	c = e.calls[len(e.calls)-1]
	if args := strings.Join(c.Args, " "); c.Name != "gh" || !strings.Contains(args, "pr create --head=th/"+created.ID+" --title=Work") || !strings.Contains(args, "--base=main") {
		t.Fatalf("pr call: %+v", c.Args)
	}
	if s := e.summary(created.ID); s.PRURL != "https://github.com/o/r/pull/7" {
		t.Fatalf("the PR shows on the card: %+v", s)
	}
}

func TestReadOnlyServerChangesNothing(t *testing.T) {
	e := newEnv(t, true)
	for _, r := range []struct{ method, path, body string }{
		{"POST", e.taskPath("T-1") + "/actions/approve-plan", `{"planSha256":"` + e.planSHA() + `"}`},
		{"POST", e.taskPath("T-1") + "/comments", `{"body":"x"}`},
		{"POST", "/api/projects/" + e.pid + "/sessions", `{}`},
		{"POST", "/api/pair/code", ""},
		{"PUT", "/api/settings", `{}`},
		{"POST", "/api/token/rotate", ""},
	} {
		if code := e.code(r.method, r.path, r.body, asShell); code != 403 {
			t.Errorf("%s %s on a read-only server: %d", r.method, r.path, code)
		}
	}
	if e.authCount() != 0 {
		t.Fatal("a read-only server never shows the OS dialog")
	}
	if snap := e.snapshot(); !snap.Me.ReadOnly || len(snap.Tasks) != 1 {
		t.Fatalf("reading still works: %+v", snap.Me)
	}
	if code := e.code("POST", "/api/login", "", asToken); code != 204 {
		t.Fatalf("a browser can still sign in to read: %d", code)
	}
}

func TestSessionsTakeNoCommandsOrDirectories(t *testing.T) {
	e := newEnv(t, false)
	for _, body := range []string{
		`{"command":"sh"}`,
		`{"args":["--dangerously-skip-permissions"]}`,
		`{"cwd":"/"}`,
		`{"permissionMode":"bypassPermissions"}`,
	} {
		if code := e.act("T-1", "start-agent", body, asToken, nil); code != 400 {
			t.Errorf("%s: %d", body, code)
		}
	}
	if code := e.code("POST", "/api/projects/"+e.pid+"/sessions", `{"cwd":"/"}`, asToken); code != 400 {
		t.Errorf("free session with a directory: %d", code)
	}
	if code := e.act("T-1", "run-shell", "", asToken, nil); code != 404 {
		t.Errorf("unknown action: %d", code)
	}
	list, _ := e.s.Agents.Sessions()
	if len(list) != 0 {
		t.Fatal("no session may start from a refused request")
	}
}

func TestOneSessionPerCheckoutWithoutWorktree(t *testing.T) {
	e := newEnv(t, false)
	var res actResult
	if code := e.act("T-1", "start-agent", "", asToken, &res); code != 201 || res.SessionID == "" {
		t.Fatalf("dev: %d", code)
	}
	if code := e.act("T-1", "start-review", "", asToken, nil); code != 409 {
		t.Fatalf("a second session in the main checkout: %d", code)
	}
	if code := e.act("T-1", "open-agent", "", asToken, &res); code != 200 || res.SessionID == "" {
		t.Fatalf("open-agent names the live session: %d", code)
	}
	created := e.newWorktreeTask("Work")
	if code := e.act(created.ID, "start-agent", "", asToken, nil); code != 201 {
		t.Fatalf("dev in the worktree: %d", code)
	}
	if code := e.act(created.ID, "start-review", "", asToken, nil); code != 201 {
		t.Fatalf("review beside dev in the task's worktree: %d", code)
	}
	if code := e.act(created.ID, "start-review", "", asToken, nil); code != 409 {
		t.Fatalf("a second review of the same task: %d", code)
	}
	if s := e.summary(created.ID); len(s.SessionIDs) != 2 {
		t.Fatalf("sessions on the card: %+v", s)
	}
}

func TestReviewNeverContinuesAWorker(t *testing.T) {
	e := newEnv(t, false)
	worker := agents.NewUUID()
	ws, _ := core.Open(e.root)
	ws.Store.AppendEvent("T-1", board.Event{Type: "moved", Actor: "claude:" + worker, To: board.Building})
	tk, _ := ws.Store.Load("T-1")
	tk.Roles = map[string]string{"developer": "claude:" + worker}
	ws.Store.Save(tk)
	e.s.Store.PutSession(appstate.Session{ID: "s_0001", Project: e.pid, Task: "T-1", Role: "review", ClaudeSessionID: worker, Status: appstate.StatusExited, Cwd: e.root, PermissionMode: "default", Resumable: true})
	if code := e.code("POST", "/api/sessions/s_0001/resume", "", asToken); code != 409 {
		t.Fatalf("a review session that built the task must not be resumed: %d", code)
	}
	e.s.Store.PutSession(appstate.Session{ID: "s_0002", Project: e.pid, Task: "T-1", Role: "review", ClaudeSessionID: agents.NewUUID(), Status: appstate.StatusExited, Cwd: e.root, PermissionMode: "default", Resumable: true})
	if code := e.code("POST", "/api/sessions/s_0002/resume", "", asToken); code != 200 {
		t.Fatalf("an independent review session resumes: %d", code)
	}
}

func TestStatusHook(t *testing.T) {
	e := newEnv(t, false)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/hook/status", strings.NewReader(`{"hook_event_name":"Stop"}`))
	r.RemoteAddr = "100.64.1.2:5555"
	r.Host = "127.0.0.1:7878"
	e.s.Handler().ServeHTTP(rec, r)
	if rec.Code != 403 {
		t.Fatalf("status hook from another machine: %d", rec.Code)
	}
	resp, _ := e.req("POST", "/hook/status", `{"hook_event_name":"Stop"}`, anon)
	if resp.StatusCode != 404 {
		t.Fatalf("unknown session: %d", resp.StatusCode)
	}
}

func readUntil(t *testing.T, ctx context.Context, c *websocket.Conn, want string) string {
	t.Helper()
	var b strings.Builder
	for !strings.Contains(b.String(), want) {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for %q: %v (got %q)", want, err, b.String())
		}
		b.Write(data)
	}
	return b.String()
}

func TestPTYOverWebSocket(t *testing.T) {
	e := newEnv(t, false)
	var sess sessionView
	if code := e.json("POST", "/api/projects/"+e.pid+"/sessions", `{"permissionMode":"plan"}`, asToken, &sess); code != 201 || sess.Role != "free" {
		t.Fatalf("free session: %d %+v", code, sess)
	}
	if code := e.code("POST", "/api/projects/"+e.pid+"/sessions", ``, asToken); code != 409 {
		t.Fatalf("one session per checkout without a worktree: %d", code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/api/sessions/" + sess.ID + "/pty"

	h := http.Header{"Authorization": {"Bearer " + testToken}, "Origin": {"http://evil.example"}}
	if _, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: h}); err == nil || resp == nil || resp.StatusCode != 403 {
		t.Fatalf("cross-origin WebSocket must be refused: %v", err)
	}
	if _, resp, err := websocket.Dial(ctx, url, nil); err == nil || resp == nil || resp.StatusCode != 401 {
		t.Fatalf("WebSocket without credentials: %v", err)
	}
	cookie := e.pairDevice()
	h = http.Header{"Cookie": {cookieName + "=" + cookie}, "Origin": {e.srv.URL}}
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	_, hello, err := c.Read(ctx)
	if err != nil || !strings.Contains(string(hello), `"t":"hello"`) {
		t.Fatalf("hello: %s %v", hello, err)
	}
	readUntil(t, ctx, c, "ARGS:")
	c.Write(ctx, websocket.MessageText, []byte(`{"t":"resize","c":90,"r":20}`))
	c.Write(ctx, websocket.MessageText, []byte(`{"t":"in","d":"ping\r"}`))
	readUntil(t, ctx, c, "GOT:ping")
	if p, _ := e.s.Agents.Proc(sess.ID); p != nil {
		if cols, rows := p.Size(); cols != 90 || rows != 20 {
			t.Fatalf("resize: %d×%d", cols, rows)
		}
	}
	var live sessionView
	for _, x := range e.snapshot().Sessions {
		if x.ID == sess.ID {
			live = x
		}
	}
	if len(live.Tail) == 0 || live.Tail[len(live.Tail)-1] != "GOT:ping" {
		t.Fatalf("the session's tail: %+v", live.Tail)
	}
	c.Write(ctx, websocket.MessageText, []byte(`{"t":"in","d":"quit\r"}`))
	readUntil(t, ctx, c, `"t":"exit"`)

	resp, b := e.req("GET", "/api/sessions/"+sess.ID+"/scrollback", "", asToken)
	if resp.StatusCode != 200 || !strings.Contains(string(b), "GOT:ping") {
		t.Fatalf("scrollback: %d %q", resp.StatusCode, b)
	}
	snap := e.snapshot()
	// No hook ever reported a prompt, so there is no conversation to resume.
	if len(snap.Sessions) != 1 || snap.Sessions[0].Status != "exited" || snap.Sessions[0].PermissionMode != "plan" || snap.Sessions[0].Resumable {
		t.Fatalf("sessions: %+v", snap.Sessions)
	}
}

func TestTaskFlow(t *testing.T) {
	e := newEnv(t, false)
	var created struct {
		taskSummary
		TicketFetchError string `json:"ticketFetchError"`
	}
	if code := e.json("POST", "/api/projects/"+e.pid+"/tasks", `{"title":"Add login","description":"Users log in with email."}`, asToken, &created); code != 201 {
		t.Fatalf("create: %d", code)
	}
	id := created.ID
	wtDir, _ := filepath.EvalSymlinks(e.s.WorktreesDir)
	if id != "T-2" || !strings.HasPrefix(created.Worktree, wtDir) || created.TicketFetchError != "" || created.ProjectID != e.pid || created.ReadOnly {
		t.Fatalf("created: %+v", created)
	}
	if code := e.code("POST", "/api/projects/"+e.pid+"/tasks", `{"title":"x","externalTicket":"file:/etc/passwd"}`, asToken); code != 400 {
		t.Fatalf("a file ticket outside the repo: %d", code)
	}
	snap := e.snapshot()
	if len(snap.Projects) != 1 || snap.Projects[0].Key != "T" || !snap.Projects[0].Active || snap.Projects[0].Branch != "main" || snap.Projects[0].ClientID != "acme" {
		t.Fatalf("projects: %+v", snap.Projects)
	}
	if len(snap.Clients) != 1 || snap.Clients[0].Name != "Acme" {
		t.Fatalf("clients: %+v", snap.Clients)
	}
	n := 0
	for _, s := range snap.Tasks {
		if s.ID == id {
			n++
			if s.Worktree != created.Worktree || s.Branch != "" {
				t.Fatalf("the worktree task is read live: %+v", s)
			}
		}
	}
	if n != 1 || len(snap.Tasks) != 2 {
		t.Fatalf("each task once: %+v", snap.Tasks)
	}
	os.WriteFile(filepath.Join(created.Worktree, "login.go"), []byte("package x\n"), 0o644)
	var d taskDetail
	e.json("GET", e.taskPath(id), "", asToken, &d)
	if strings.TrimSpace(d.Description) != "Users log in with email." || len(d.Diff) != 1 || d.Diff[0].Path != "login.go" || d.Diff[0].Status != "A" {
		t.Fatalf("detail: %+v", d)
	}
	if len(d.Checks) == 0 || d.Checks[0].Gate != "approved" {
		t.Fatalf("checks: %+v", d.Checks)
	}
	var patch struct {
		Path, Patch string
		Truncated   bool
	}
	if code := e.json("GET", e.taskPath(id)+"/diff?file=login.go", "", asToken, &patch); code != 200 || !strings.Contains(patch.Patch, "package x") {
		t.Fatalf("patch on demand: %d %+v", code, patch)
	}
	if code := e.code("GET", e.taskPath(id)+"/diff?file=nope.go", "", asToken); code != 404 {
		t.Fatalf("patch of an unchanged file: %d", code)
	}
	if _, err := os.Stat(filepath.Join(created.Worktree, ".truehand/tasks", id, "context.md")); err != nil {
		t.Fatal("a description is fetched into context.md")
	}
	var c commentView
	if code := e.json("POST", e.taskPath(id)+"/comments", `{"body":"Use the design system"}`, asToken, &c); code != 201 || c.Body != "Use the design system" || c.Actor.Kind != "human" || c.Actor.Name != "H" {
		t.Fatalf("comment: %d %+v", code, c)
	}
	if code := e.code("GET", e.taskPath("T-1")+"/files/evidence/x.png", "", asToken); code != 200 {
		t.Fatalf("evidence file: %d", code)
	}
	if code := e.code("GET", e.taskPath("T-1")+"/files/..%2F..%2Fconfig.yaml", "", asToken); code == 200 {
		t.Fatal("path traversal must be refused")
	}
	var sum taskSummary
	if code := e.json("PATCH", e.taskPath(id), `{"title":"Add login form"}`, asToken, &sum); code != 200 || sum.Title != "Add login form" {
		t.Fatalf("edit: %d %+v", code, sum)
	}
	if code := e.code("DELETE", e.taskPath(id)+"/worktree", "", asToken); code != 409 {
		t.Fatalf("a worktree with uncommitted work is kept without force: %d", code)
	}
	if code := e.code("DELETE", e.taskPath(id)+"/worktree?force=1", "", asToken); code != 204 {
		t.Fatalf("forced removal: %d", code)
	}
}

func TestDeliverMovesThroughTheGate(t *testing.T) {
	e := newEnv(t, false)
	ws, _ := core.Open(e.root)
	tk, _ := ws.Store.Load("T-1")
	tk.State = board.Reviewed
	ws.Store.Save(tk)
	var res actResult
	if code := e.act("T-1", "deliver", "", asToken, &res); code != 200 || !res.OK {
		t.Fatalf("an open gate delivers: %d %+v", code, res)
	}
	if e.state("T-1") != board.Delivered {
		t.Fatal("not delivered")
	}
	os.WriteFile(filepath.Join(e.root, ".truehand/config.yaml"), []byte("version: 0\nbase: main\ngates:\n  verified:\n    - check: plan_valid\n    - check: human_approval\n"), 0o644)
	tk.State = board.Approved
	ws.Store.Save(tk)
	if code := e.act("T-1", "start-agent", "", asToken, nil); code != 201 {
		t.Fatalf("start: %d", code)
	}
	tk.State = board.Building
	ws.Store.Save(tk)
	var closed actResult
	resp, b := e.req("POST", e.taskPath("T-1")+"/actions/deliver", "", asToken)
	json.Unmarshal(b, &closed)
	if resp.StatusCode != 409 && resp.StatusCode != 400 {
		t.Fatalf("skipping states is refused: %d %s", resp.StatusCode, b)
	}
}

func TestSendBackNeedsAReason(t *testing.T) {
	e := newEnv(t, false)
	ws, _ := core.Open(e.root)
	tk, _ := ws.Store.Load("T-1")
	tk.State = board.Building
	ws.Store.Save(tk)
	if code := e.act("T-1", "send-back", `{"state":"planned"}`, asToken, nil); code != 400 {
		t.Fatalf("back without reason: %d", code)
	}
	if code := e.act("T-1", "send-back", `{"state":"verified","reason":"x"}`, asToken, nil); code != 400 {
		t.Fatalf("send-back cannot go forward: %d", code)
	}
	if code := e.act("T-1", "send-back", `{"state":"planned","reason":"the plan misses the API"}`, asToken, nil); code != 200 {
		t.Fatalf("back with reason: %d", code)
	}
	ev, _ := ws.Store.Events("T-1")
	if ev[len(ev)-2].Type != "comment" || ev[len(ev)-1].Type != "reworked" {
		t.Fatalf("the reason is saved as a comment before the move: %+v", ev[len(ev)-2:])
	}
}

func TestSettings(t *testing.T) {
	e := newEnv(t, false)
	st := e.snapshot().Settings
	if st.DefaultPermissionMode != "default" || st.Remote.Enabled || st.Language != "it" {
		t.Fatalf("defaults: %+v", st)
	}
	var got settingsView
	if code := e.json("PUT", "/api/settings", `{"language":"en","theme":"dark","defaultPermissionMode":"acceptEdits"}`, asToken, &got); code != 200 || got.Language != "en" || got.DefaultPermissionMode != "acceptEdits" {
		t.Fatalf("language and mode: %d %+v", code, got)
	}
	if code := e.code("PUT", "/api/settings", `{"defaultPermissionMode":"bypassPermissions"}`, asToken); code != 400 {
		t.Fatalf("no permission bypass: %d", code)
	}
	tail := `{"remote":{"enabled":true,"mode":"tailnet","address":"100.101.102.103"}}`
	if code := e.code("PUT", "/api/settings", tail, asToken); code != 403 || e.authCount() != 0 {
		t.Fatalf("opening remote access needs the person at the Mac: %d", code)
	}
	if code := e.code("PUT", "/api/settings", `{"remote":{"enabled":true,"mode":"tailnet","address":"0.0.0.0"}}`, asShell); code != 400 {
		t.Fatalf("all interfaces: %d", code)
	}
	if code := e.json("PUT", "/api/settings", tail, asShell, &got); code != 200 || e.authCount() != 1 || !got.Remote.Enabled || got.Remote.Address != "100.101.102.103" {
		t.Fatalf("tailnet from the Mac: %d %+v", code, got.Remote)
	}
}

func TestEventsStream(t *testing.T) {
	e := newEnv(t, false)
	stop := make(chan struct{})
	defer close(stop)
	go e.s.Watch(stop, 50*time.Millisecond)
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/events", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Scan()
	time.Sleep(150 * time.Millisecond)
	ws, _ := core.Open(e.root)
	ws.Comment("T-1", "hello")
	got := make(chan string, 1)
	go func() {
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "data: "+e.pid) {
				got <- sc.Text()
				return
			}
		}
	}()
	select {
	case <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("no change event")
	}
}

func TestStaticAppAndHeaders(t *testing.T) {
	e := newEnv(t, false)
	resp, b := e.req("GET", "/tasks/T-1", "", anon)
	if resp.StatusCode != 200 || !strings.Contains(string(b), "make app") {
		t.Fatalf("placeholder: %d %s", resp.StatusCode, b)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("csp: %q", csp)
	}
	resp, _ = e.req("GET", e.taskPath("T-1")+"/files/evidence/x.png", "", asToken)
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatal("task files are served sandboxed")
	}
}

func TestSnapshotIsCamelCase(t *testing.T) {
	e := newEnv(t, false)
	_, b := e.req("GET", "/api/snapshot", "", asToken)
	for _, k := range []string{`"projectId"`, `"waitReason"`, `"primaryAction"`, `"sessionIds"`, `"readOnly"`, `"defaultPermissionMode"`, `"clientId"`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("missing %s in %s", k, b)
		}
	}
	if strings.Contains(string(b), "_") && strings.Contains(string(b), `"project_`) {
		t.Errorf("snake_case leaked: %s", b)
	}
}

func TestQueuedActionsExpireAndFollowThePlan(t *testing.T) {
	e := newEnv(t, false)
	var q actResult
	if code := e.act("T-1", "approve-plan", `{"planSha256":"`+e.planSHA()+`"}`, asToken, &q); code != 202 {
		t.Fatalf("queue: %d", code)
	}
	if len(e.snapshot().Queued) != 1 {
		t.Fatal("queued")
	}
	ws, _ := core.Open(e.root)
	os.WriteFile(ws.Store.PlanPath("T-1"), []byte(goodPlan+"\nedited after the request\n"), 0o644)
	if left := e.snapshot().Queued; len(left) != 0 {
		t.Fatalf("an approval of a plan that changed must leave the queue: %+v", left)
	}
	if code := e.act("T-1", "push", "", asToken, nil); code != 400 {
		t.Fatalf("push of the base branch: %d", code)
	}
	created := e.newWorktreeTask("Work")
	if code := e.act(created.ID, "push", "", asToken, &q); code != 202 {
		t.Fatalf("queue push: %d", code)
	}
	e.s.Store.UpdateAuth(func(a *appstate.Auth) error {
		a.Queued[0].Time = time.Now().Add(-25 * time.Hour)
		return nil
	})
	if code := e.code("POST", "/api/queued/"+q.QueuedID+"/confirm", "", asShell); code != 404 || e.authCount() != 0 {
		t.Fatalf("an expired request cannot be confirmed: %d", code)
	}
	if left := e.snapshot().Queued; len(left) != 0 {
		t.Fatalf("a request older than a day must expire: %+v", left)
	}
}

func TestConfigChangesNeedThePersonAtTheMac(t *testing.T) {
	e := newEnv(t, false)
	yaml := "version: 0\nbase: main\ngates:\n  approved:\n    - check: plan_valid\n    - check: human_approval\n"
	body := `{"yaml":` + strconvQuote(yaml) + `}`
	cookie := e.pairDevice()
	before := e.authCount()
	var q actResult
	for _, c := range []cred{asToken, {cookie: cookie}} {
		if code := e.json("PUT", "/api/projects/"+e.pid+"/config", body, c, &q); code != 202 || !q.Queued {
			t.Errorf("%+v: %d", c, code)
		}
	}
	if snap := e.snapshot(); len(snap.Queued) != 1 || snap.Queued[0].Kind != "config" || e.authCount() != before {
		t.Fatalf("one config change waits for the Mac: %+v", snap.Queued)
	}
	if code := e.code("POST", "/api/queued/"+q.QueuedID+"/confirm", "", cred{cookie: cookie}); code != 403 {
		t.Fatalf("a phone confirms a config change: %d", code)
	}
	if code := e.code("POST", "/api/queued/"+q.QueuedID+"/confirm", "", asShell); code != 200 || e.authCount() != before+1 {
		t.Fatalf("confirm on the Mac: %d", code)
	}
	before = e.authCount()
	if code := e.code("POST", "/api/projects/"+e.pid+"/activate", `{"forceHooks":true}`, asToken); code != 403 {
		t.Fatalf("replacing foreign hooks with the token alone: %d", code)
	}
	if code := e.code("PUT", "/api/projects/"+e.pid+"/config", `{"yaml":"gates: ["}`, asShell); code != 400 || e.authCount() != before {
		t.Fatalf("an invalid config is refused before any dialog: %d", code)
	}
	var a activationView
	if code := e.json("PUT", "/api/projects/"+e.pid+"/config", body, asShell, &a); code != 200 || !a.HasConfig || e.authCount() != before+1 {
		t.Fatalf("from the Mac: %d %+v", code, a)
	}
}

func TestSessionStuckStartingWaitsForInput(t *testing.T) {
	e := newEnv(t, false)
	e.s.Store.PutSession(appstate.Session{ID: "s_00a1", Project: e.pid, Task: "T-1", Role: "dev", Status: appstate.StatusStarting, Started: time.Now().Add(-time.Minute), Cwd: e.root, PermissionMode: "default"})
	e.s.Store.PutSession(appstate.Session{ID: "s_00a2", Project: e.pid, Role: "free", Status: appstate.StatusStarting, Started: time.Now(), Cwd: e.root, PermissionMode: "default"})
	e.s.Agents = nil
	snap := e.snapshot()
	got := map[string]sessionView{}
	for _, x := range snap.Sessions {
		got[x.ID] = x
	}
	if got["s_00a1"].Status != "waiting" || got["s_00a1"].WaitingFor != "input" {
		t.Fatalf("stuck in starting (folder trust prompt): %+v", got["s_00a1"])
	}
	if got["s_00a2"].Status != "starting" {
		t.Fatalf("just started: %+v", got["s_00a2"])
	}
	if s := e.summary("T-1"); s.WaitReason != waitPermission || s.PrimaryAction != actOpenAgent {
		t.Fatalf("a trust prompt blocks the agent like a permission prompt: %+v", s)
	}
}

func TestShutdownOnlyFromTheMacApp(t *testing.T) {
	e := newEnv(t, false)
	stopped := make(chan struct{}, 1)
	e.s.Shutdown = func() { stopped <- struct{}{} }
	cookie := e.pairDevice()
	before := e.authCount()
	for _, c := range []cred{asToken, {cookie: cookie}, {cookie: cookie, shell: true}} {
		if code := e.code("POST", "/api/shutdown", "", c); code != 403 {
			t.Errorf("%+v: %d", c, code)
		}
	}
	if code := e.code("POST", "/api/shutdown", "", asShell); code != 204 {
		t.Fatalf("app backend: %d", code)
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("not stopped")
	}
	if e.authCount() != before {
		t.Fatal("stopping shows no dialog")
	}
}

func TestShouldsFromReview(t *testing.T) {
	e := newEnv(t, false)
	for _, p := range []string{"", "evidence", "evidence/"} {
		if code := e.code("GET", e.taskPath("T-1")+"/files/"+p, "", asToken); code != 404 {
			t.Errorf("directory %q listed: %d", p, code)
		}
	}
	created := e.newWorktreeTask("-x --draft")
	if code := e.act(created.ID, "open-pr", "", asShell, nil); code != 200 {
		t.Fatalf("pr: %d", code)
	}
	c := e.calls[len(e.calls)-1]
	for _, a := range c.Args {
		if a == "-x --draft" || a == "--draft" {
			t.Fatalf("the title became an option: %q", c.Args)
		}
	}
	e.s.Store.PutSession(appstate.Session{ID: "s_0c0c", Project: e.pid, Task: "T-1", Role: "dev", ClaudeSessionID: agents.NewUUID(), Status: appstate.StatusExited, Cwd: e.root, PermissionMode: "default", Resumable: true})
	var wg sync.WaitGroup
	codes := make([]int, 4)
	for i := range codes {
		wg.Add(1)
		go func(i int) { defer wg.Done(); codes[i] = e.code("POST", "/api/sessions/s_0c0c/resume", "", asToken) }(i)
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		if c == 200 {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("concurrent resumes of one session: %v", codes)
	}
}

func TestCloseEndsEventStreams(t *testing.T) {
	e := newEnv(t, false)
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/events", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	done := make(chan struct{})
	go func() { io.Copy(io.Discard, resp.Body); close(done) }()
	e.s.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("an event stream outlived the server's shutdown")
	}
}

func TestShellChallenge(t *testing.T) {
	e := newEnv(t, false)
	e.s.ShellSecretHash = appstate.HashSecret("s3cret")
	var got struct{ Mac string }
	if code := e.json("POST", "/api/shell/challenge", `{"nonce":"`+strings.Repeat("00", 32)+`"}`, anon, &got); code != 200 {
		t.Fatalf("challenge without credentials: %d", code)
	}
	if got.Mac != "a3c4a924ce728a8fe665fa04adbc071e6ca391a450cf3cd1ee706c98743a3b67" {
		t.Fatalf("mac %s does not match the Mac app's reference value", got.Mac)
	}
	for _, n := range []string{"", "00", strings.Repeat("0G", 32), strings.Repeat("AA", 32)} {
		if code := e.code("POST", "/api/shell/challenge", `{"nonce":"`+n+`"}`, anon); code != 400 {
			t.Errorf("nonce %q: %d", n, code)
		}
	}
	e.s.ShellSecretHash = ""
	if code := e.code("POST", "/api/shell/challenge", `{"nonce":"`+strings.Repeat("00", 32)+`"}`, anon); code != 404 {
		t.Fatalf("a server started without the app: %d", code)
	}
}

func TestStoppedSessionIsNotAFailure(t *testing.T) {
	e := newEnv(t, false)
	e.s.Agents = nil
	ws, _ := core.Open(e.root)
	tk, _ := ws.Store.Load("T-1")
	tk.State = board.Building
	ws.Store.Save(tk)
	code := 143
	now := time.Now().UTC()
	e.s.Store.PutSession(appstate.Session{ID: "s_00f1", Project: e.pid, Task: "T-1", Role: "dev", Status: appstate.StatusExited, ExitCode: &code, Ended: &now, Updated: now, Started: now, Stopped: true, Resumable: true, ClaudeSessionID: agents.NewUUID()})
	if s := e.summary("T-1"); s.Failing || s.PrimaryAction != actResumeAgent {
		t.Fatalf("a session the user stopped: %+v", s)
	}
	crash := 3
	later := now.Add(time.Second)
	e.s.Store.PutSession(appstate.Session{ID: "s_00f2", Project: e.pid, Task: "T-1", Role: "dev", Status: appstate.StatusCrashed, ExitCode: &crash, Ended: &later, Updated: later, Started: later, Resumable: true, ClaudeSessionID: agents.NewUUID()})
	if s := e.summary("T-1"); !s.Failing {
		t.Fatalf("a session that crashed: %+v", s)
	}
}

func TestDetailShowsTheApprovalAtOnce(t *testing.T) {
	e := newEnv(t, false)
	created := e.newWorktreeTask("Work")
	wws, _ := core.OpenWorktree(created.Worktree)
	os.WriteFile(wws.Store.PlanPath(created.ID), []byte(goodPlan), 0o644)
	for _, id := range []string{"T-1", created.ID} {
		var d taskDetail
		e.json("GET", e.taskPath(id), "", asToken, &d)
		if code := e.act(id, "approve-plan", `{"planSha256":"`+d.PlanSHA256+`"}`, asShell, nil); code != 200 {
			t.Fatalf("%s approve: %d", id, code)
		}
		e.json("GET", e.taskPath(id), "", asToken, &d)
		if d.State != board.Approved {
			t.Fatalf("%s: the detail right after approving says %s", id, d.State)
		}
		if s := e.summary(id); s.State != board.Approved {
			t.Fatalf("%s: the snapshot right after approving says %s", id, s.State)
		}
	}
}

func TestCommitConfig(t *testing.T) {
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
	os.WriteFile(filepath.Join(dir, ".truehand/config.yaml"), []byte("version: 0\nbase: main\ngates:\n  approved:\n    - check: plan_valid\n    - check: human_approval\n"), 0o644)
	var p projectView
	if code := e.json("POST", "/api/projects", `{"path":"`+dir+`","clientName":"Acme"}`, asToken, &p); code != 201 {
		t.Fatalf("project: %d", code)
	}
	resp, b := e.req("POST", "/api/projects/"+p.ID+"/tasks", `{"title":"x"}`, asToken)
	if resp.StatusCode != 409 || !strings.Contains(string(b), codeConfigNotCommitted) {
		t.Fatalf("a worktree task before the config is committed: %d %s", resp.StatusCode, b)
	}
	os.WriteFile(filepath.Join(dir, "other.txt"), []byte("x"), 0o644)
	git(t, dir, "add", "other.txt")
	if resp, b := e.req("POST", "/api/projects/"+p.ID+"/commit-config", `{"configSha256":"`+evidence.HashBytes([]byte("version: 0\nbase: main\ngates:\n  approved:\n    - check: plan_valid\n    - check: human_approval\n"))+`"}`, asShell); resp.StatusCode != 409 || !strings.Contains(string(b), "other-changes-staged") || e.authCount() != 0 {
		t.Fatalf("other staged changes: %d %s", resp.StatusCode, b)
	}
	git(t, dir, "reset", "-q", "other.txt")
	var a activationView
	e.json("GET", "/api/projects/"+p.ID+"/activation", "", asToken, &a)
	shown := `{"configSha256":"` + a.ConfigSHA256 + `"}`
	if code := e.code("POST", "/api/projects/"+p.ID+"/commit-config", `{"configSha256":"stale"}`, asShell); code != 409 {
		t.Fatalf("a config other than the one shown: %d", code)
	}
	var q actResult
	if code := e.json("POST", "/api/projects/"+p.ID+"/commit-config", shown, asToken, &q); code != 202 || !q.Queued {
		t.Fatalf("from a phone or with the token alone it waits for the Mac: %d", code)
	}
	if qs := e.snapshot().Queued; len(qs) != 1 || qs[0].Kind != "commit-config" || qs[0].ConfigSHA256 != a.ConfigSHA256 {
		t.Fatalf("the queue says which config was asked for: %+v", qs)
	}
	var res actResult
	if code := e.json("POST", "/api/queued/"+q.QueuedID+"/confirm", "", asShell, &res); code != 200 || !res.OK {
		t.Fatalf("confirm on the Mac: %d %+v", code, res)
	}
	if last := e.auths[len(e.auths)-1]; !strings.Contains(last, "No approvers") || !strings.Contains(last, "(sha "+a.ConfigSHA256[:12]+")") {
		t.Fatalf("the dialog at confirmation describes the queued config: %q", last)
	}
	if log := git(t, dir, "log", "-1", "--format=%s", "--name-only"); !strings.Contains(log, "chore(truehand): add truehand config") || !strings.Contains(log, ".truehand/config.yaml") || strings.Contains(log, "other.txt") {
		t.Fatalf("commit: %s", log)
	}
	if code := e.code("POST", "/api/projects/"+p.ID+"/tasks", `{"title":"x"}`, asToken); code != 201 {
		t.Fatalf("a worktree task after the commit: %d", code)
	}
	if resp, b := e.req("POST", "/api/projects/"+p.ID+"/commit-config", shown, asShell); resp.StatusCode != 409 || !strings.Contains(string(b), "nothing-to-commit") {
		t.Fatalf("nothing left to commit: %d %s", resp.StatusCode, b)
	}
}

func TestReadyPlanWinsOverAnIdleAgent(t *testing.T) {
	e := newEnv(t, false)
	e.s.Agents = nil
	now := time.Now().UTC()
	e.s.Store.PutSession(appstate.Session{ID: "s_00e1", Project: e.pid, Task: "T-1", Role: "dev", Status: appstate.StatusWaitingInput, Started: now, Updated: now, Cwd: e.root, PermissionMode: "default", ClaudeSessionID: agents.NewUUID()})
	if s := e.summary("T-1"); !s.PlanReady || s.WaitReason != waitApprovePlan || s.PrimaryAction != actApprovePlan {
		t.Fatalf("the agent stopped after writing the plan: %+v", s)
	}
	e.s.Store.PutSession(appstate.Session{ID: "s_00e1", Project: e.pid, Task: "T-1", Role: "dev", Status: appstate.StatusWaitingPermission, Started: now, Updated: now, Cwd: e.root, PermissionMode: "default", ClaudeSessionID: agents.NewUUID()})
	if s := e.summary("T-1"); s.WaitReason != waitPermission {
		t.Fatalf("a permission prompt still comes first: %+v", s)
	}
}

// The tester's flow: closing the dev session from the app (SIGTERM, exit 143) must leave the
// task waiting for its next human step, not "blocked".
func TestClosingASessionDoesNotBlockTheTask(t *testing.T) {
	e := newEnv(t, false)
	var res actResult
	if code := e.act("T-1", "start-agent", "", asToken, &res); code != 201 {
		t.Fatalf("start: %d", code)
	}
	p, _ := e.s.Agents.Proc(res.SessionID)
	if code := e.code("POST", "/api/sessions/"+res.SessionID+"/signal", `{"sig":"term"}`, asToken); code != 204 {
		t.Fatalf("close: %d", code)
	}
	<-p.Done()
	rec, _ := e.s.Agents.Get(res.SessionID)
	if rec.ExitCode == nil || *rec.ExitCode == 0 || rec.Status != appstate.StatusExited || !rec.Stopped {
		t.Fatalf("a session closed from the app: %+v", rec)
	}
	if s := e.summary("T-1"); s.Failing || s.WaitReason != waitApprovePlan {
		t.Fatalf("the task after closing its session: %+v", s)
	}
	for _, x := range e.snapshot().Sessions {
		if x.ID == res.SessionID && !x.StoppedByUser {
			t.Fatalf("the UI must know the session was stopped on request: %+v", x)
		}
	}
}

func TestCommitConfigShowsWhoBecomesApprover(t *testing.T) {
	e := newEnv(t, false)
	e.s.Run = runCmd
	_, pub := newKey(t)
	indented := "      " + strings.ReplaceAll(strings.TrimSpace(pub), "\n", "\n      ")
	cfg := "version: 0\nbase: main\napprovers:\n  - id: mac\n    name: Emanuel\n    public_key: |\n" + indented + "\ngates:\n  approved:\n    - check: plan_valid\n    - check: human_approval\n"
	os.WriteFile(filepath.Join(e.root, ".truehand/config.yaml"), []byte(cfg), 0o644)
	var a activationView
	e.json("GET", "/api/projects/"+e.pid+"/activation", "", asToken, &a)
	if len(a.ApproversAdded) != 1 || a.ApproversAdded[0].ID != "mac" || len(a.Approvers) != 1 || !strings.Contains(a.ConfigDiff, "+approvers:") || !strings.Contains(a.ConfigDiff, "--- base/.truehand/config.yaml") || !strings.Contains(a.ConfigDiff, "+++ working/.truehand/config.yaml") {
		t.Fatalf("activation: %+v", a)
	}
	if code := e.code("POST", "/api/projects/"+e.pid+"/commit-config", `{"configSha256":"`+a.ConfigSHA256+`"}`, asShell); code != 200 {
		t.Fatalf("commit: %d", code)
	}
	fp := a.ApproversAdded[0].Key
	if fp != keyFingerprint(pub) {
		t.Fatal("activation's key fingerprint")
	}
	if last := e.auths[len(e.auths)-1]; !strings.Contains(last, "1 approver(s): mac (\"Emanuel\", key "+fp+")") || !strings.Contains(last, "Adds approver(s) mac (\"Emanuel\", key "+fp+")") || !strings.Contains(last, "(sha "+a.ConfigSHA256[:12]+")") || len(fp) != 12 {
		t.Fatalf("the dialog must say who becomes an approver, with the key: %q", last)
	}
	// A name with a line break must not write its own lines into the dialog, and removing
	// an approver is said too.
	_, otherPub := newKey(t)
	spoof := strings.Replace(cfg, "id: mac\n    name: Emanuel", "id: mac2\n    name: \"Emanuel\\nNo approvers.\"", 1)
	spoof = strings.Replace(spoof, indented, "      "+strings.ReplaceAll(strings.TrimSpace(otherPub), "\n", "\n      "), 1)
	os.WriteFile(filepath.Join(e.root, ".truehand/config.yaml"), []byte(spoof), 0o644)
	var b activationView
	e.json("GET", "/api/projects/"+e.pid+"/activation", "", asToken, &b)
	// The Mac app says which key is its own: the "Emanuel" added with another key is foreign.
	body, _ := json.Marshal(map[string]string{"configSha256": b.ConfigSHA256, "thisMacKey": pub})
	if code := e.code("POST", "/api/projects/"+e.pid+"/commit-config", string(body), asShell); code != 200 {
		t.Fatalf("commit: %d", code)
	}
	last := e.auths[len(e.auths)-1]
	if strings.Contains(last, "\n") || !strings.Contains(last, "Adds approver(s) mac2 (\"Emanuel No approvers.\", key ") || !strings.Contains(last, ", foreign key)") || !strings.Contains(last, "Removes approver(s) mac (\"Emanuel\", key "+fp+", this Mac)") {
		t.Fatalf("spoofed name: %q", last)
	}
	var after activationView
	e.json("GET", "/api/projects/"+e.pid+"/activation", "", asToken, &after)
	if len(after.ApproversAdded) != 0 || after.ConfigDiff != "" || len(after.Approvers) != 1 {
		t.Fatalf("after the commit nothing differs from the base: %q %+v", after.ConfigDiff, after.ApproversAdded)
	}
}

func TestRemoteAccessChangesWithoutRestart(t *testing.T) {
	e := newEnv(t, false)
	var applied []appstate.Remote
	e.s.ApplyRemote = func(rm appstate.Remote) ([]string, error) {
		applied = append(applied, rm)
		if rm.Mode == "tailnet" {
			return []string{rm.Addr, "mac.example-tail.ts.net", "mac"}, nil
		}
		return nil, nil
	}
	host := cred{token: true, host: "mac.example-tail.ts.net:7941"}
	if code := e.code("GET", "/api/me", "", host); code != 403 {
		t.Fatalf("before: %d", code)
	}
	if code := e.code("PUT", "/api/settings", `{"remote":{"enabled":true,"mode":"tailnet","address":"100.64.0.1"}}`, asShell); code != 200 || len(applied) != 1 {
		t.Fatalf("enable: %d %v", code, applied)
	}
	for _, h := range []string{"mac.example-tail.ts.net:7941", "mac:7941", "100.64.0.1:7941"} {
		if code := e.code("GET", "/api/me", "", cred{token: true, host: h}); code != 200 {
			t.Errorf("%s right after enabling: %d", h, code)
		}
	}
	if code := e.code("PUT", "/api/settings", `{"remote":{"enabled":false}}`, asShell); code != 200 {
		t.Fatalf("disable: %d", code)
	}
	if code := e.code("GET", "/api/me", "", host); code != 403 {
		t.Fatalf("after disabling: %d", code)
	}
	e.s.ApplyRemote = func(appstate.Remote) ([]string, error) { return nil, errString("address not available") }
	resp, b := e.req("PUT", "/api/settings", `{"remote":{"enabled":true,"mode":"tailnet","address":"100.70.216.86"}}`, asShell)
	if resp.StatusCode != 502 || !strings.Contains(string(b), "remote-unavailable") {
		t.Fatalf("a listener that cannot start is reported: %d %s", resp.StatusCode, b)
	}
}

func TestPushAndPRPreconditions(t *testing.T) {
	e := newEnv(t, false)
	created := e.newWorktreeTask("Work")
	code := func(action string) string {
		resp, b := e.req("POST", e.taskPath(created.ID)+"/actions/"+action, "", asShell)
		if resp.StatusCode != 409 {
			return fmt.Sprint(resp.StatusCode)
		}
		var v struct{ Code string }
		json.Unmarshal(b, &v)
		return v.Code
	}
	e.s.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if c := code("open-pr"); c != "gh-missing" {
		t.Fatalf("no gh: %s", c)
	}
	git(t, e.root, "remote", "set-url", "origin", "/srv/git/demo.git")
	if c := code("open-pr"); c != "remote-not-github" {
		t.Fatalf("local origin: %s", c)
	}
	git(t, e.root, "remote", "remove", "origin")
	if c := code("push"); c != "no-remote" {
		t.Fatalf("no origin: %s", c)
	}
	if e.authCount() != 0 || len(e.calls) != 0 {
		t.Fatal("refused before any dialog or command")
	}
}

func TestSendBackNotifiesTheAgentWithoutASecondComment(t *testing.T) {
	e := newEnv(t, false)
	var res actResult
	e.act("T-1", "start-agent", "", asToken, &res)
	ws, _ := core.Open(e.root)
	tk, _ := ws.Store.Load("T-1")
	tk.State = board.Building
	ws.Store.Save(tk)
	var back actResult
	if code := e.act("T-1", "send-back", `{"state":"planned","reason":"the plan misses the API","notifyAgent":true}`, asToken, &back); code != 200 || back.SessionID != res.SessionID {
		t.Fatalf("send-back: %d %+v", code, back)
	}
	var d taskDetail
	e.json("GET", e.taskPath("T-1"), "", asToken, &d)
	if n := len(d.Comments); n != 1 || d.Comments[0].Body != "the plan misses the API" {
		t.Fatalf("only the reason is a comment: %+v", d.Comments)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, _ := e.s.Agents.Scrollback(res.SessionID); strings.Contains(string(b), "went back to planned") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the dev session was not told")
}

func TestReadableActors(t *testing.T) {
	infos := map[string]core.ActorInfo{
		"claude:81aae9d0-1111-2222-3333-444455556666": {Name: "81aae9d0…", Kind: core.ActorAgent, Role: "review"},
		"human:h@x.y": {Name: "H", Kind: core.ActorHuman},
	}
	got := readableActors("review pass by claude:81aae9d0-1111-2222-3333-444455556666, asked by human:h@x.y", infos)
	if got != "review pass by the review agent, asked by H" {
		t.Fatal(got)
	}
}

func TestRemoteStatusAndAddressFromTailscale(t *testing.T) {
	e := newEnv(t, false)
	state, msg := "off", ""
	e.s.RemoteStatus = func() (string, string) { return state, msg }
	e.s.TailnetIP = func() string { return "100.64.0.1" }
	e.s.ApplyRemote = func(rm appstate.Remote) ([]string, error) {
		if rm.Mode == "tailnet" {
			state = "listening"
			return []string{rm.Addr}, nil
		}
		state = "off"
		return nil, nil
	}
	var got settingsView
	if code := e.json("PUT", "/api/settings", `{"remote":{"enabled":true,"mode":"tailnet"}}`, asShell, &got); code != 200 {
		t.Fatalf("enable without an address: %d", code)
	}
	if got.Remote.Address != "100.64.0.1" || got.Remote.Status != "listening" {
		t.Fatalf("remote: %+v", got.Remote)
	}
	state, msg = "error", "listen tcp 100.64.0.1:7941: bind: can't assign requested address"
	e.json("GET", "/api/settings", "", asToken, &got)
	if got.Remote.Status != "error" || !strings.Contains(got.Remote.Error, "bind") {
		t.Fatalf("the real state is reported: %+v", got.Remote)
	}
	e.s.TailnetIP = func() string { return "" }
	e.code("PUT", "/api/settings", `{"remote":{"enabled":false}}`, asShell)
	if code := e.code("PUT", "/api/settings", `{"remote":{"enabled":true,"mode":"tailnet","address":""}}`, asShell); code != 400 {
		t.Fatalf("no Tailscale: %d", code)
	}
}

func TestConfigCommittedButNotPushed(t *testing.T) {
	e := newEnv(t, false)
	remote := filepath.Join(t.TempDir(), "origin.git")
	git(t, filepath.Dir(remote), "init", "-q", "--bare", "-b", "main", remote)
	dir := filepath.Join(t.TempDir(), "app")
	os.MkdirAll(filepath.Join(dir, ".truehand"), 0o755)
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "config", "user.email", "h@x.y")
	git(t, dir, "config", "user.name", "H")
	os.WriteFile(filepath.Join(dir, "README"), []byte("x"), 0o644)
	git(t, dir, "add", "README")
	git(t, dir, "commit", "-q", "-m", "chore: init")
	git(t, dir, "remote", "add", "origin", remote)
	git(t, dir, "push", "-q", "origin", "main")
	os.WriteFile(filepath.Join(dir, ".truehand/config.yaml"), []byte("version: 0\nbase: origin/main\ngates:\n  approved:\n    - check: plan_valid\n"), 0o644)
	git(t, dir, "add", ".truehand/config.yaml")
	git(t, dir, "commit", "-q", "-m", "chore(truehand): add truehand config")
	var p projectView
	e.json("POST", "/api/projects", `{"path":"`+dir+`","clientName":"Acme"}`, asToken, &p)
	resp, b := e.req("POST", "/api/projects/"+p.ID+"/tasks", `{"title":"x"}`, asToken)
	if resp.StatusCode != 409 || !strings.Contains(string(b), codeConfigNotPushed) || !strings.Contains(string(b), "push main") {
		t.Fatalf("committed but not pushed: %d %s", resp.StatusCode, b)
	}
	git(t, dir, "push", "-q", "origin", "main")
	git(t, dir, "fetch", "-q", "origin")
	if code := e.code("POST", "/api/projects/"+p.ID+"/tasks", `{"title":"x"}`, asToken); code != 201 {
		t.Fatalf("after the push: %d", code)
	}
}

func TestGhReason(t *testing.T) {
	out := []byte("Warning: 2 uncommitted changes\nTo get started with GitHub CLI, please run:  gh auth login\n")
	if got := ghReason(out); !strings.Contains(got, "gh auth login") {
		t.Fatal(got)
	}
}

func TestGhFailureHasACode(t *testing.T) {
	e := newEnv(t, false)
	created := e.newWorktreeTask("Work")
	e.s.Run = func(ctx context.Context, d string, env []string, name string, args ...string) ([]byte, error) {
		return []byte("To get started with GitHub CLI, please run:  gh auth login\n"), errString("exit status 4")
	}
	var res actResult
	if code := e.act(created.ID, "open-pr", "", asShell, &res); code != 502 || res.Code != "gh-failed" || !strings.Contains(res.Reason, "gh auth login") {
		t.Fatalf("gh failure: %d %+v", code, res)
	}
}

// The board cache must not hide a change made outside this server.
func TestCachedBoardSeesChangesAtOnce(t *testing.T) {
	e := newEnv(t, false)
	if s := e.summary("T-1"); s.State != board.Planned {
		t.Fatal(s.State)
	}
	ws, _ := core.Open(e.root)
	tk, _ := ws.Store.Load("T-1")
	tk.State = board.Building
	ws.Store.Save(tk)
	if s := e.summary("T-1"); s.State != board.Building {
		t.Fatalf("a task changed on disk: the snapshot still says %s", s.State)
	}
}

// A tool that hangs (gh asking the network, a broken claude) must never hold up the board.
func TestSlowToolProbesNeverBlockTheSnapshot(t *testing.T) {
	e := newEnv(t, false)
	e.s.LookPath = func(name string) (string, error) { return "/bin/" + name, nil }
	e.s.mu.Lock()
	e.s.health, e.s.probed, e.s.probing, e.s.waitedProbe = nil, nil, false, false
	e.s.mu.Unlock()
	release := make(chan struct{})
	defer close(release)
	e.s.Run = func(ctx context.Context, d string, env []string, name string, args ...string) ([]byte, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, ctx.Err()
	}
	start := time.Now()
	e.snapshot()
	if d := time.Since(start); d > 4*time.Second {
		t.Fatalf("first snapshot waited %v for the tools", d)
	}
	start = time.Now()
	e.snapshot()
	if d := time.Since(start); d > time.Second {
		t.Fatalf("a later snapshot waited %v for the tools", d)
	}
}
