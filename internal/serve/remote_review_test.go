package serve

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/spaghetticoders/truehand/internal/appstate"

	"github.com/spaghetticoders/truehand/internal/machines"
)

// A view-only caller on the Mac (an agent on the socket) must not type into a remote
// machine's terminal by going through the Mac's forwarding.
func TestReviewViewOnlyCannotTypeIntoRemotePTY(t *testing.T) {
	re := newRemoteEnv(t)
	m := re.addMachine()
	peerRepo := repo(t)
	var p projectView
	if code := re.json("POST", "/api/projects", `{"path":"`+peerRepo+`","clientName":"Acme","machineId":"`+m.ID+`"}`, asToken, &p); code != 201 {
		t.Fatalf("remote project: %d", code)
	}
	var sess sessionView
	if code := re.json("POST", "/api/projects/"+p.ID+"/sessions", `{}`, asToken, &sess); code != 201 {
		t.Fatalf("remote session: %d", code)
	}
	sock := httptest.NewServer(re.s.SocketHandler())
	defer sock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(sock.URL, "http")+"/api/sessions/"+sess.ID+"/pty", nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	readUntil(t, ctx, c, "ARGS:")
	c.Write(ctx, websocket.MessageText, []byte(`{"t":"in","d":"typed-by-agent\r"}`))
	rctx, rcancel := context.WithTimeout(ctx, 3*time.Second)
	defer rcancel()
	for {
		_, b, err := c.Read(rctx)
		if err != nil {
			return
		}
		if strings.Contains(string(b), "GOT:typed-by-agent") {
			t.Fatal("a view-only caller typed into a remote session's terminal through the Mac")
		}
	}
}

// A forged remote id cannot climb out of /api/projects on the peer.
func TestReviewRemoteIDCannotEscape(t *testing.T) {
	re := newRemoteEnv(t)
	m := re.addMachine()
	for _, raw := range []string{
		"/api/projects/" + m.ID + "~x/../../settings",
		"/api/projects/" + m.ID + "~..%2F..%2Fsettings/tasks",
		"/api/sessions/" + m.ID + "~../../devices",
	} {
		conn, err := net.Dial("tcp", strings.TrimPrefix(re.srv.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(conn, "GET "+raw+" HTTP/1.1\r\nHost: 127.0.0.1\r\nAuthorization: Bearer "+testToken+"\r\nConnection: close\r\n\r\n")
		resp, err := http.ReadResponse(bufioReader(conn), nil)
		if err != nil {
			conn.Close()
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		conn.Close()
		if resp.StatusCode == 200 && (strings.Contains(string(b), "remote") || strings.Contains(string(b), "lang")) {
			t.Errorf("%s reached the peer's %s: %s", raw, "settings/devices", b)
		}
	}
}

func bufioReader(c net.Conn) *bufio.Reader { return bufio.NewReader(c) }

// fakePeerRT answers every forwarded request with a fixed response, as a hostile peer would.
type fakePeerRT struct {
	status int
	header http.Header
	body   string
}

func (f fakePeerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	h := http.Header{}
	for k, v := range f.header {
		h[k] = v
	}
	return &http.Response{StatusCode: f.status, Header: h, Body: io.NopCloser(strings.NewReader(f.body)), Request: r}, nil
}

func hostilePeer(t *testing.T, e *env, rt http.RoundTripper) string {
	t.Helper()
	e.s.Remotes = &Remotes{SSH: &machines.SSH{Bin: "/nonexistent"}}
	m, _ := e.s.Store.AddMachine(appstateMachine("evil.example"))
	e.s.Remotes.get(m).peer.HTTP.Transport = rt
	return m.ID
}

func TestReviewHostilePeerCannotActOnTheMacOrigin(t *testing.T) {
	e := newEnv(t, false)
	mid := hostilePeer(t, e, fakePeerRT{status: 200, header: http.Header{
		"Content-Type":                {"text/html"},
		"Set-Cookie":                  {cookieName + "=stolen; Path=/"},
		"Content-Security-Policy":     {"default-src *"},
		"Access-Control-Allow-Origin": {"*"},
		"Clear-Site-Data":             {`"cookies"`},
	}, body: "<script>fetch('/api/token/rotate',{method:'POST'})</script>"})
	resp, _ := e.req("GET", "/api/projects/"+mid+"~p_1/tasks/T-1/files/evidence/x.html", "", asToken)
	if resp.Header.Get("Set-Cookie") != "" || resp.Header.Get("Access-Control-Allow-Origin") != "" || resp.Header.Get("Clear-Site-Data") != "" {
		t.Fatalf("a peer's headers reached the Mac's origin: %v", resp.Header)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") || strings.Contains(csp, "default-src *") {
		t.Fatalf("a peer's HTML must be sandboxed: %q", csp)
	}
}

func TestReviewForwardedPRBoundToTheSignature(t *testing.T) {
	e := newEnv(t, false)
	signedHead := strings.Repeat("a", 40)
	pr := `{"ok":false,"prRequest":{"repo":"github.com/evil/other","head":"th/T-1","title":"x","body":"y"}}`
	mid := hostilePeer(t, e, fakePeerRT{status: 200, header: http.Header{"Content-Type": {"application/json"}}, body: pr})
	var ghCalls [][]string
	githubHead := signedHead // what GitHub says the branch points at
	e.s.Run = func(ctx context.Context, d string, env []string, name string, args ...string) ([]byte, error) {
		ghCalls = append(ghCalls, args)
		if len(args) > 0 && args[0] == "api" {
			return []byte(githubHead), nil
		}
		return []byte("https://github.com/evil/other/pull/1\n"), nil
	}
	mac, _ := newKey(t)
	e.s.MacKey = &mac.PublicKey
	fresh := func() string { return signedBody(t, mac, openPRIntent(signedHead, time.Now())) }
	signed := fresh()
	path := "/api/projects/" + mid + "~p_1/tasks/T-1/actions/open-pr"
	for name, body := range map[string]string{"another repo": signed, "no signature": `{}`} {
		if name == "another repo" {
			body = fresh()
		}
		var res actResult
		e.json("POST", path, body, asToken, &res)
		if res.OK || res.URL != "" || (res.Code != "pr-mismatch" && res.Code != "pr-not-signed") {
			t.Errorf("%s: %+v", name, res)
		}
	}
	// Right repo, but the branch moved since the signature.
	githubHead = strings.Repeat("b", 40)
	e.s.Remotes.get(appstateMachineByID(t, e, mid)).peer.HTTP.Transport = fakePeerRT{status: 200, header: http.Header{"Content-Type": {"application/json"}}, body: strings.Replace(pr, "github.com/evil/other", "github.com/acme/app", 1)}
	var res actResult
	e.json("POST", path, fresh(), asToken, &res)
	if res.OK || res.Code != "pr-mismatch" {
		t.Fatalf("a branch not at the signed commit: %+v", res)
	}
	for _, c := range ghCalls {
		if len(c) > 1 && c[0] == "pr" && c[1] == "create" {
			t.Fatalf("gh pr create ran for a PR the person did not sign: %v", c)
		}
	}
}

func TestReviewPeerImageURLsAreRouted(t *testing.T) {
	out := string(prefixIDs([]byte(`{"proofs":[{"images":[{"url":"/api/projects/p_1/tasks/T-1/files/evidence/a.png","caption":"a"}]}],"prUrl":"https://github.com/o/r/pull/1","path":"/Users/me/app"}`), "m_ab"))
	if !strings.Contains(out, `"/api/projects/m_ab~p_1/tasks/T-1/files/evidence/a.png"`) || !strings.Contains(out, `"https://github.com/o/r/pull/1"`) || !strings.Contains(out, `"/Users/me/app"`) {
		t.Fatal(out)
	}
}

func appstateMachineByID(t *testing.T, e *env, mid string) appstate.Machine {
	t.Helper()
	m, ok := e.s.machineByID(mid)
	if !ok {
		t.Fatal("machine")
	}
	return m
}

func TestShellRevokesAnotherDevice(t *testing.T) {
	e := newEnv(t, false)
	cookie := e.pairDevice()
	var devs []deviceView
	e.json("GET", "/api/devices", "", asToken, &devs)
	if code := e.code("DELETE", "/api/devices/"+devs[0].ID, "", asShell); code != 204 {
		t.Fatalf("the Mac app revoking a phone: %d", code)
	}
	if code := e.code("GET", "/api/me", "", cred{cookie: cookie}); code != 401 {
		t.Fatalf("revoked phone: %d", code)
	}
}

func TestShellUpdatesAMachine(t *testing.T) {
	re := newRemoteEnv(t)
	m := re.addMachine()
	before := re.authCount()
	var got machineView
	if code := re.json("POST", "/api/machines/"+m.ID+"/update", "", asShell, &got); code != 200 || got.Status != machineOnline {
		t.Fatalf("update from the Mac app: %d %+v", code, got)
	}
	if re.authCount() != before+1 {
		t.Fatal("updating a machine asks the person once")
	}
	if code := re.code("POST", "/api/machines/"+m.ID+"/update", "", asToken); code != 403 {
		t.Fatalf("update with the token alone: %d", code)
	}
}

func TestShellReplacesForeignHooks(t *testing.T) {
	e := newEnv(t, false)
	hooks := filepath.Join(e.root, ".git", "hooks")
	os.MkdirAll(hooks, 0o755)
	os.WriteFile(filepath.Join(hooks, "pre-push"), []byte("#!/bin/sh\necho someone else's hook\n"), 0o755)
	if code := e.code("POST", "/api/projects/"+e.pid+"/activate", `{"forceHooks":true}`, asToken); code != 403 {
		t.Fatalf("token alone: %d", code)
	}
	var a activationView
	if code := e.json("POST", "/api/projects/"+e.pid+"/activate", `{"forceHooks":true}`, asShell, &a); code != 200 || a.GitHooks["pre-push"] != "ours" {
		t.Fatalf("the Mac app replacing a foreign hook: %d %+v", code, a.GitHooks)
	}
}
