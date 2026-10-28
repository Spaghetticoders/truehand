package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/core"
)

func approvalsOf(t *testing.T, root, id string) int {
	t.Helper()
	ws, err := core.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := ws.Store.Approvals(id)
	return len(a)
}

// With the token or a phone's cookie, and no app secret, nothing human-only happens and the
// OS dialog never appears.
func TestReviewAttackerWithoutShellSecret(t *testing.T) {
	e := newEnv(t, false)
	cookie := e.pairDevice()
	base := e.authCount()
	for _, c := range []cred{asToken, {cookie: cookie}, {token: true, cookie: cookie}} {
		for _, r := range []struct{ method, path, body string }{
			{"POST", "/api/pair/code", ""},
			{"POST", "/api/token/rotate", ""},
			{"PUT", "/api/settings", `{"remote":{"enabled":true,"mode":"tailnet","address":"100.100.1.1"}}`},
			{"POST", e.taskPath("T-1") + "/actions/deliver", ""},
		} {
			if code := e.code(r.method, r.path, r.body, c); code < 300 || code == 202 {
				t.Errorf("%+v %s %s: %d", c, r.method, r.path, code)
			}
		}
	}
	if e.authCount() != base {
		t.Fatalf("the OS dialog appeared %d times without the app's secret", e.authCount()-base)
	}
	if approvalsOf(t, e.root, "T-1") != 0 || e.state("T-1") != board.Planned {
		t.Fatal("approved without a person at the Mac")
	}
	if code := e.code("POST", e.taskPath("T-1")+"/actions/deliver", "", asShell); code != 409 {
		t.Fatalf("skipping to delivered must be refused: %d", code)
	}
}

func TestReviewWrongShellSecretShowsNoDialog(t *testing.T) {
	e := newEnv(t, false)
	body := `{"planSha256":"` + e.planSHA() + `"}`
	req, _ := http.NewRequest("POST", e.srv.URL+e.taskPath("T-1")+"/actions/approve-plan", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set(shellHeader, "shell-guess")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 202 || e.authCount() != 0 {
		t.Fatalf("wrong shell secret: %d, %d dialogs", resp.StatusCode, e.authCount())
	}
}

func TestReviewQueuedApprovalOfAChangedPlanIsDropped(t *testing.T) {
	e := newEnv(t, false)
	var q actResult
	if code := e.act("T-1", "approve-plan", `{"planSha256":"`+e.planSHA()+`"}`, asToken, &q); code != 202 {
		t.Fatalf("queue: %d", code)
	}
	ws, _ := core.Open(e.root)
	os.WriteFile(ws.Store.PlanPath("T-1"), []byte(goodPlan+"\nchanged by the agent\n"), 0o644)
	if code := e.code("POST", "/api/queued/"+q.QueuedID+"/confirm", "", asShell); code != 409 {
		t.Fatalf("confirming an approval of a plan that changed: %d", code)
	}
	if e.authCount() != 0 || approvalsOf(t, e.root, "T-1") != 0 {
		t.Fatal("dialog shown or approval recorded for a plan the human never saw")
	}
}

func TestReviewPairingCodeExpires(t *testing.T) {
	e := newEnv(t, false)
	var pc struct{ Code string }
	e.json("POST", "/api/pair/code", "", asShell, &pc)
	e.s.mu.Lock()
	for h := range e.s.pairs {
		e.s.pairs[h] = time.Now().Add(-time.Second)
	}
	e.s.mu.Unlock()
	if code := e.code("POST", "/api/pair", `{"code":"`+pc.Code+`","deviceName":"late"}`, anon); code != 401 {
		t.Fatalf("expired pairing code accepted: %d", code)
	}
}

func TestReviewFilesStayInTheTaskDir(t *testing.T) {
	e := newEnv(t, false)
	ws, _ := core.Open(e.root)
	dir := ws.Store.TaskDir("T-1")
	secret := filepath.Join(t.TempDir(), "secret.txt")
	os.WriteFile(secret, []byte("TOP-SECRET"), 0o600)
	os.Symlink(secret, filepath.Join(dir, "evidence", "link.png"))
	os.Symlink(filepath.Dir(secret), filepath.Join(dir, "evidence", "dirlink"))
	for _, p := range []string{
		"evidence/link.png",
		"evidence/dirlink/secret.txt",
		"../../config.yaml",
		"..%2F..%2Fconfig.yaml",
		"%2e%2e/%2e%2e/config.yaml",
		"evidence/../../../config.yaml",
		"/etc/passwd",
	} {
		resp, b := e.req("GET", e.taskPath("T-1")+"/files/"+p, "", asToken)
		if resp.StatusCode == 200 && (strings.Contains(string(b), "TOP-SECRET") || strings.Contains(string(b), "gates:")) {
			t.Errorf("%s leaked a file outside the task dir", p)
		}
	}
	resp, _ := e.req("GET", e.taskPath("T-1")+"/files/evidence/x.png", "", asToken)
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("evidence file: %d %q", resp.StatusCode, resp.Header.Get("Content-Security-Policy"))
	}
}

func TestReviewFSDirsStaysUnderHome(t *testing.T) {
	e := newEnv(t, false)
	os.Symlink("/", filepath.Join(e.s.Home, "root"))
	for _, p := range []string{"/etc", "/", filepath.Join(e.s.Home, "root"), filepath.Join(e.s.Home, ".."), "relative"} {
		if code := e.code("GET", "/api/fs/dirs?path="+p, "", asToken); code == 200 {
			t.Errorf("%s listed", p)
		}
	}
}

func TestReviewPTYRefusesCrossSiteCookie(t *testing.T) {
	e := newEnv(t, false)
	var sess struct {
		ID string `json:"id"`
	}
	if code := e.json("POST", "/api/projects/"+e.pid+"/sessions", `{}`, asToken, &sess); code != 201 {
		t.Fatalf("session: %d", code)
	}
	cookie := e.pairDevice()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/api/sessions/" + sess.ID + "/pty"
	for _, origin := range []string{"http://evil.example", "null", ""} {
		h := http.Header{"Cookie": {cookieName + "=" + cookie}}
		if origin != "" {
			h.Set("Origin", origin)
		}
		c, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: h})
		if err == nil {
			c.CloseNow()
			t.Errorf("cookie WebSocket with Origin %q accepted", origin)
		} else if resp == nil || resp.StatusCode != 403 {
			t.Errorf("Origin %q: %v", origin, err)
		}
	}
}

func TestReviewPromptCannotBecomeAFlag(t *testing.T) {
	e := newEnv(t, false)
	var sess struct {
		ID string `json:"id"`
	}
	if code := e.json("POST", "/api/projects/"+e.pid+"/sessions", `{"prompt":"--dangerously-skip-permissions"}`, asToken, &sess); code != 201 {
		t.Fatalf("session: %d", code)
	}
	rec, err := e.s.Agents.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	i := -1
	for k, a := range rec.Args {
		if a == "--dangerously-skip-permissions" {
			i = k
		}
	}
	if i < 1 || rec.Args[i-1] != "--" || i != len(rec.Args)-1 {
		t.Fatalf("the prompt must be the last argument, after --: %v", rec.Args)
	}
}

// Rewriting the gates is as strong as approving: without a person at the Mac, the token or a
// phone must not be able to drop human_approval and let an agent move on unapproved.
func TestReviewTokenCannotDropTheApprovalGate(t *testing.T) {
	e := newEnv(t, false)
	yaml := "version: 0\nbase: main\ngates:\n  approved:\n    - check: plan_valid\n"
	body := `{"yaml":` + strconvQuote(yaml) + `}`
	code := e.code("PUT", "/api/projects/"+e.pid+"/config", body, asToken)
	ws, _ := core.Open(e.root)
	tk, _ := ws.Store.Load("T-1")
	t.Setenv("TRUEHAND_ACTOR", "claude:agent")
	if _, err := ws.Move(tk, board.Approved); err == nil {
		t.Fatalf("PUT config with the token alone answered %d, and the agent then moved T-1 to approved with no human approval", code)
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestReviewShutdownNeedsTheShell(t *testing.T) {
	e := newEnv(t, false)
	stopped := make(chan struct{}, 4)
	e.s.Shutdown = func() { stopped <- struct{}{} }
	cookie := e.pairDevice()
	for _, c := range []cred{asToken, {cookie: cookie}, {cookie: cookie, shell: true}} {
		if code := e.code("POST", "/api/shutdown", "", c); code != 403 {
			t.Errorf("%+v: %d", c, code)
		}
	}
	if code := e.code("POST", "/api/shutdown", "", asShell); code != 204 {
		t.Fatalf("the Mac app: %d", code)
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("not stopped")
	}
	if len(stopped) != 0 {
		t.Fatal("stopped more than once")
	}
}
