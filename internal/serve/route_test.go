package serve

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/spaghetticoders/truehand/internal/appstate"
	"github.com/spaghetticoders/truehand/internal/machines"
)

func appstateMachine(target string) appstate.Machine { return appstate.Machine{SSHTarget: target} }

const fakeSSH = `#!/bin/sh
while [ $# -gt 0 ]; do
  case "$1" in
    -o) shift 2 ;;
    -*) shift ;;
    *) break ;;
  esac
done
target=$1; shift
case "$target" in
  down.example) echo "ssh: connect to host down.example port 22: Connection refused" >&2; exit 255 ;;
  nokey.example) echo "Permission denied (publickey)." >&2; exit 255 ;;
esac
# Like sshd, the command does not descend from whoever runs ssh: these tests may run inside
# an agent session, and the peer must not see its callers as that agent.
# The remote exit status is lost; these tests do not rely on it.
# A background job reads /dev/null unless stdin is redirected explicitly.
( ( sleep 0.2; exec env -i HOME="$PEER_HOME" SHELL="$PEER_SHELL" PATH="$PEER_PATH" /bin/sh -c "$1" ) <&0 & )
`

const fakeLoginShell = `#!/bin/sh
[ "$1" = "-lc" ] && shift
exec /bin/sh -c "$1"
`

var (
	buildOnce sync.Once
	builtBin  string
	buildErr  error
)

// truehandBinary builds the real binary once: the peer runs it.
func truehandBinary(t *testing.T) string {
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "truehand-bin")
		if err != nil {
			buildErr = err
			return
		}
		builtBin = filepath.Join(dir, "truehand")
		out, err := exec.Command("go", "build", "-o", builtBin, "github.com/spaghetticoders/truehand/cmd/truehand").CombinedOutput()
		if err != nil {
			buildErr = err
			t.Logf("%s", out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return builtBin
}

type remoteEnv struct {
	*env
	peerHome string
	mid      string
}

// newRemoteEnv is a Mac server plus a "remote" machine that is this computer under another
// home, reached through a stand-in for ssh.
func newRemoteEnv(t *testing.T) *remoteEnv {
	bin := truehandBinary(t)
	e := newEnv(t, false)
	tools := t.TempDir()
	os.WriteFile(filepath.Join(tools, "ssh"), []byte(fakeSSH), 0o755)
	os.WriteFile(filepath.Join(tools, "loginsh"), []byte(fakeLoginShell), 0o755)
	os.WriteFile(filepath.Join(tools, "claude"), []byte(fakeClaude), 0o755)
	// A short home: the peer's unix socket path must fit in sun_path.
	peerHome, err := os.MkdirTemp("/tmp", "thp")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PEER_HOME", peerHome)
	t.Setenv("PEER_SHELL", filepath.Join(tools, "loginsh"))
	t.Setenv("PEER_PATH", tools+":/usr/bin:/bin")
	e.s.Version = "0.0.0-dev"
	e.s.Remotes = &Remotes{SSH: &machines.SSH{Bin: filepath.Join(tools, "ssh")}, BinaryFor: func(string, string) (string, error) { return bin, nil }}
	re := &remoteEnv{env: e, peerHome: peerHome}
	t.Cleanup(func() {
		if re.mid != "" {
			if m, ok := e.s.machineByID(re.mid); ok {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				e.s.Remotes.get(m).peer.Stop(ctx)
				cancel()
			}
		}
		time.Sleep(300 * time.Millisecond)
		if b, err := os.ReadFile(filepath.Join(peerHome, ".truehand", "app", "serve.lock")); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		os.RemoveAll(peerHome)
	})
	return re
}

func (re *remoteEnv) addMachine() machineView {
	re.t.Helper()
	var m machineView
	if code := re.json("POST", "/api/machines", `{"sshTarget":"mini.example","label":"Mac mini"}`, asShell, &m); code != 201 {
		re.t.Fatalf("add machine: %d %+v", code, m)
	}
	re.mid = m.ID
	return m
}

func TestRemoteMachine(t *testing.T) {
	re := newRemoteEnv(t)
	if code := re.code("POST", "/api/machines", `{"sshTarget":"mini.example"}`, asToken); code != 403 {
		t.Fatalf("adding a machine needs the person at the Mac: %d", code)
	}
	if code := re.code("POST", "/api/machines", `{"sshTarget":"-oProxyCommand=sh"}`, asShell); code != 400 {
		t.Fatalf("an option as target: %d", code)
	}
	m := re.addMachine()
	if m.Status != machineOnline || m.Version != "0.0.0-dev" || !m.Tools["claude"] || !m.Tools["git"] || m.CanApprove {
		t.Fatalf("machine: %+v", m)
	}
	if fi, err := os.Stat(filepath.Join(re.peerHome, ".truehand", "app", "serve.sock")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("peer socket: %v %v", fi, err)
	}

	peerRepo := repo(t)
	var p projectView
	if code := re.json("POST", "/api/projects", `{"path":"`+peerRepo+`","clientName":"Acme","machineId":"`+m.ID+`"}`, asToken, &p); code != 201 {
		t.Fatalf("remote project: %d", code)
	}
	if !strings.HasPrefix(p.ID, m.ID+"~p_") || p.MachineID != m.ID || !p.Active {
		t.Fatalf("remote project: %+v", p)
	}
	snap := re.snapshot()
	if len(snap.Machines) != 2 || snap.Machines[0].ID != "local" || snap.Machines[1].Status != machineOnline {
		t.Fatalf("machines: %+v", snap.Machines)
	}
	var remoteTask *taskSummary
	for i := range snap.Tasks {
		if snap.Tasks[i].ProjectID == p.ID {
			remoteTask = &snap.Tasks[i]
		}
	}
	if remoteTask == nil || remoteTask.WaitReason != waitApprovePlan {
		t.Fatalf("remote tasks in the snapshot: %+v", snap.Tasks)
	}
	if len(snap.Clients) != 1 {
		t.Fatalf("one client across machines: %+v", snap.Clients)
	}

	var d taskDetail
	if code := re.json("GET", "/api/projects/"+p.ID+"/tasks/T-1", "", asToken, &d); code != 200 || d.ProjectID != p.ID || d.PlanSHA256 == "" {
		t.Fatalf("remote detail: %d %+v", code, d.taskSummary)
	}
	resp, b := re.req("POST", "/api/projects/"+p.ID+"/tasks/T-1/actions/approve-plan", `{"planSha256":"`+d.PlanSHA256+`"}`, asShell)
	if resp.StatusCode != 409 || !strings.Contains(string(b), "remote-approval-unavailable") || re.authCount() != 1 {
		t.Fatalf("a remote approval without signatures: %d %s (dialogs %d)", resp.StatusCode, b, re.authCount())
	}

	stop := make(chan struct{})
	defer close(stop)
	go re.s.Watch(stop, 100*time.Millisecond)
	req, _ := http.NewRequest("GET", re.srv.URL+"/api/events", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	ev, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Body.Close()
	changed := make(chan string, 8)
	go func() {
		sc := bufio.NewScanner(ev.Body)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "data: "+m.ID+"~") {
				changed <- sc.Text()
			}
		}
	}()
	if code := re.code("POST", "/api/projects/"+p.ID+"/tasks/T-1/comments", `{"body":"from the Mac"}`, asToken); code != 201 {
		t.Fatalf("remote comment: %d", code)
	}
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("no change event relayed from the peer")
	}

	var sess sessionView
	if code := re.json("POST", "/api/projects/"+p.ID+"/sessions", `{}`, asToken, &sess); code != 201 || !strings.HasPrefix(sess.ID, m.ID+"~s_") || sess.MachineID != m.ID {
		t.Fatalf("remote session: %d %+v", code, sess)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(re.srv.URL, "http") + "/api/sessions/" + sess.ID + "/pty"
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + testToken}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	readUntil(t, ctx, c, "ARGS:")
	c.Write(ctx, websocket.MessageText, []byte(`{"t":"in","d":"over ssh\r"}`))
	readUntil(t, ctx, c, "GOT:over ssh")
	if code := re.code("POST", "/api/sessions/"+sess.ID+"/signal", `{"sig":"term"}`, asToken); code != 204 {
		t.Fatalf("remote signal: %d", code)
	}
	readUntil(t, ctx, c, `"t":"exit"`)
}

func TestUnreachableMachines(t *testing.T) {
	re := newRemoteEnv(t)
	for target, want := range map[string]string{"down.example": "refused", "nokey.example": "key"} {
		resp, b := re.req("POST", "/api/machines", `{"sshTarget":"`+target+`"}`, asShell)
		if resp.StatusCode != 502 || !strings.Contains(string(b), want) {
			t.Errorf("%s: %d %s", target, resp.StatusCode, b)
		}
	}
	if snap := re.snapshot(); len(snap.Machines) != 1 {
		t.Fatalf("an unreachable machine is not kept: %+v", snap.Machines)
	}
	if code := re.code("GET", "/api/projects/m_00000000~p_1/tasks/T-1", "", asToken); code != 404 {
		t.Fatalf("unknown machine: %d", code)
	}
}

func TestPrefixIDs(t *testing.T) {
	in := `{"projects":[{"id":"p_1","clientId":"acme","path":"/x"}],"tasks":[{"id":"T-1","projectId":"p_1","sessionIds":["s_1"]}],"sessions":[{"id":"s_1","projectId":"p_1","startedAt":"2026-10-27T00:00:00Z","taskId":"T-1"}],"sessionId":"s_1"}`
	out := string(prefixIDs([]byte(in), "m_ab"))
	for _, want := range []string{`"id":"m_ab~p_1"`, `"machineId":"m_ab"`, `"projectId":"m_ab~p_1"`, `"sessionIds":["m_ab~s_1"]`, `"id":"m_ab~s_1"`, `"id":"T-1"`, `"taskId":"T-1"`, `"sessionId":"m_ab~s_1"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
	if got := string(unprefixBody([]byte(`{"sessionId":"m_ab~s_1","sendToSession":"m_ab~s_2","body":"m_ab~x"}`), "m_ab")); !strings.Contains(got, `"sessionId":"s_1"`) || !strings.Contains(got, `"sendToSession":"s_2"`) || !strings.Contains(got, `"body":"m_ab~x"`) {
		t.Fatal(got)
	}
}

type captureRT struct {
	mu  sync.Mutex
	got []*http.Request
}

func (c *captureRT) RoundTrip(r *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.got = append(c.got, r)
	c.mu.Unlock()
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: http.NoBody, Request: r}, nil
}

// A remote machine gets the request, never the Mac's token, cookie, app secret or Origin.
func TestForwardKeepsCredentialsOnTheMac(t *testing.T) {
	e := newEnv(t, false)
	e.s.Remotes = &Remotes{SSH: &machines.SSH{Bin: "/nonexistent"}}
	m, _ := e.s.Store.AddMachine(appstateMachine("mini.example"))
	rt := &captureRT{}
	e.s.Remotes.get(m).peer.HTTP.Transport = rt
	cookie := e.pairDevice()
	e.code("POST", "/api/projects/"+m.ID+"~p_1/tasks/T-1/comments", `{"body":"x","sendToSession":"`+m.ID+`~s_9"}`, cred{cookie: cookie, shell: true})
	e.code("GET", "/api/sessions/"+m.ID+"~s_9/scrollback?x=1", "", asShell)
	if len(rt.got) != 2 {
		t.Fatalf("forwarded %d requests", len(rt.got))
	}
	for _, r := range rt.got {
		for _, h := range []string{"Cookie", "Authorization", "Origin", shellHeader} {
			if r.Header.Get(h) != "" {
				t.Errorf("%s %s carried %s", r.Method, r.URL.Path, h)
			}
		}
	}
	if rt.got[0].URL.Path != "/api/projects/p_1/tasks/T-1/comments" || rt.got[1].URL.Path != "/api/sessions/s_9/scrollback" || rt.got[1].URL.RawQuery != "x=1" {
		t.Fatalf("paths: %s %s", rt.got[0].URL, rt.got[1].URL)
	}
}

func TestSocketServesNoHumanOnlyActions(t *testing.T) {
	e := newEnv(t, false)
	h := e.s.SocketHandler()
	trusted := true
	call := func(method, path, body string) (int, string) {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r = r.WithContext(context.WithValue(r.Context(), socketTrustKey{}, trusted))
		h.ServeHTTP(rec, r)
		return rec.Code, rec.Body.String()
	}
	for _, r := range []struct{ method, path string }{
		{"POST", "/api/pair/code"}, {"POST", "/api/pair"}, {"POST", "/api/login"}, {"POST", "/api/token/rotate"},
		{"GET", "/api/machines"}, {"POST", "/api/machines"}, {"PUT", "/api/settings"}, {"GET", "/api/devices"},
	} {
		if code, _ := call(r.method, r.path, "{}"); code != 403 {
			t.Errorf("%s %s on the socket: %d", r.method, r.path, code)
		}
	}
	if code, body := call("POST", e.taskPath("T-1")+"/actions/approve-plan", `{"planSha256":"`+e.planSHA()+`"}`); code != 403 || e.authCount() != 0 {
		t.Fatalf("approve on the Mac's socket: %d %s", code, body)
	}
	e.s.Peer = true
	if code, body := call("POST", e.taskPath("T-1")+"/actions/approve-plan", `{"planSha256":"`+e.planSHA()+`"}`); code != 409 || !strings.Contains(body, "remote-approval-unavailable") || e.authCount() != 0 {
		t.Fatalf("approve on a peer: %d %s", code, body)
	}
	if code, _ := call("GET", "/api/snapshot", ""); code != 200 {
		t.Fatalf("reading on the socket: %d", code)
	}
	if q := e.snapshot().Queued; len(q) != 0 {
		t.Fatal("nothing is queued from the socket")
	}
	trusted = false
	if code, _ := call("POST", "/api/projects/"+e.pid+"/sessions", `{}`); code != 403 {
		t.Fatalf("an untrusted connection starts a session: %d", code)
	}
	if code, _ := call("GET", "/api/snapshot", ""); code != 200 {
		t.Fatalf("an untrusted connection may read: %d", code)
	}
	if _, body := call("GET", "/api/me", ""); !strings.Contains(body, `"readOnly":true`) {
		t.Fatalf("an untrusted connection is told it is read-only: %s", body)
	}
	trusted = true
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/projects/"+e.pid+"/sessions", strings.NewReader(`{}`))
	r.Header.Set(viewOnlyHeader, "1")
	h.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), socketTrustKey{}, true)))
	if rec.Code != 403 {
		t.Fatalf("a request forwarded for a watcher changes something: %d", rec.Code)
	}
}

func TestSocketTrustFollowsThePeerProcess(t *testing.T) {
	e := newEnv(t, false)
	dir, _ := os.MkdirTemp("/tmp", "ths")
	defer os.RemoveAll(dir)
	ln, err := net.Listen("unix", filepath.Join(dir, "s.sock"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: e.s.SocketHandler(), ConnContext: e.s.SocketConnContext}
	go srv.Serve(ln)
	defer srv.Close()
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(dir, "s.sock"))
	}}}
	post := func() int {
		client.CloseIdleConnections()
		resp, err := client.Post("http://socket/api/projects/"+e.pid+"/tasks/T-1/comments", "application/json", strings.NewReader(`{"body":"x"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	var seen int
	old := AgentProcess
	defer func() { AgentProcess = old }()
	AgentProcess = func(pid int) bool { seen = pid; return true }
	if code := post(); code != 403 || seen != os.Getpid() {
		t.Fatalf("an agent's connection: %d (peer pid %d, mine %d)", code, seen, os.Getpid())
	}
	AgentProcess = func(int) bool { return false }
	if code := post(); code != 201 {
		t.Fatalf("a person's connection: %d", code)
	}
	AgentProcess = nil
	if code := post(); code != 403 {
		t.Fatalf("without a detector every connection is untrusted: %d", code)
	}
}
