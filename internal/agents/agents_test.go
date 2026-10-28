package agents

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spaghetticoders/truehand/internal/appstate"
)

func TestLaunchArgs(t *testing.T) {
	id := NewUUID()
	base := LaunchSpec{Role: RoleDev, PluginDir: "/p/plugin", Settings: "/a/hooks/settings.json", ClaudeSessionID: id, PermissionMode: "default", Prompt: "-rf go"}
	got, err := LaunchArgs(base)
	if err != nil {
		t.Fatal(err)
	}
	want := "--plugin-dir /p/plugin --settings /a/hooks/settings.json --session-id " + id + " --permission-mode default -- -rf go"
	if strings.Join(got, " ") != want {
		t.Fatalf("got  %q\nwant %q", strings.Join(got, " "), want)
	}
	r := base
	r.Resume = true
	got, _ = LaunchArgs(r)
	if strings.Join(got, " ") != "--plugin-dir /p/plugin --settings /a/hooks/settings.json --resume "+id+" --permission-mode default" {
		t.Fatalf("resume: %q", got)
	}
	for name, mut := range map[string]func(*LaunchSpec){
		"yolo":         func(s *LaunchSpec) { s.PermissionMode = "bypassPermissions" },
		"empty mode":   func(s *LaunchSpec) { s.PermissionMode = "" },
		"role":         func(s *LaunchSpec) { s.Role = "shell" },
		"session id":   func(s *LaunchSpec) { s.ClaudeSessionID = "x; rm -rf /" },
		"relative dir": func(s *LaunchSpec) { s.PluginDir = "plugin" },
	} {
		s := base
		mut(&s)
		if _, err := LaunchArgs(s); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(NewUUID()) {
		t.Fatal("NewUUID is not a v4 UUID")
	}
}

func TestCleanEnv(t *testing.T) {
	env := CleanEnv([]string{"PATH=/bin", "TRUEHAND_TOKEN=abc", "TRUEHAND_ACTOR=human:x", "CLAUDECODE=1", "TERM=dumb", "HOME=/h"}, "s:k@127.0.0.1:1")
	got := strings.Join(env, " ")
	if got != "PATH=/bin HOME=/h TERM=xterm-256color COLORTERM=truecolor TRUEHAND_SESSION=s:k@127.0.0.1:1" {
		t.Fatalf("%s", got)
	}
}

func TestSessionEnv(t *testing.T) {
	sid, sec, addr, err := ParseSessionEnv(SessionEnv("s_1", "k", "127.0.0.1:7878"))
	if err != nil || sid != "s_1" || sec != "k" || addr != "127.0.0.1:7878" {
		t.Fatal(sid, sec, addr, err)
	}
	for _, bad := range []string{"", "s_1:k", "s_1@127.0.0.1:1", "s_1:k@evil.com:80", ":k@127.0.0.1:1"} {
		if _, _, _, err := ParseSessionEnv(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestRing(t *testing.T) {
	r := newRing(5)
	r.Write([]byte("ab"))
	if string(r.Bytes()) != "ab" {
		t.Fatal(string(r.Bytes()))
	}
	r.Write([]byte("cde"))
	if string(r.Bytes()) != "abcde" {
		t.Fatal(string(r.Bytes()))
	}
	r.Write([]byte("fg"))
	if string(r.Bytes()) != "cdefg" {
		t.Fatal(string(r.Bytes()))
	}
	r.Write([]byte("0123456789"))
	if string(r.Bytes()) != "56789" {
		t.Fatal(string(r.Bytes()))
	}
}

func TestTailLines(t *testing.T) {
	in := "one\r\n\x1b[1;32mtwo\x1b[0m\r\nprogress 10%\rprogress 100%\r\n\x1b]0;title\x07three\n\n  \nfour\r\n"
	got := strings.Join(TailLines([]byte(in), 3), "|")
	if got != "progress 100%|three|four" {
		t.Fatalf("%q", got)
	}
	if len(TailLines(nil, 5)) != 0 {
		t.Fatal("empty")
	}
}

func TestNextStatus(t *testing.T) {
	for _, c := range []struct {
		cur string
		in  hookInput
		out string
	}{
		{"starting", hookInput{Event: "SessionStart"}, "idle"},
		{"working", hookInput{Event: "SessionStart"}, "working"},
		{"idle", hookInput{Event: "UserPromptSubmit"}, "working"},
		{"waiting_permission", hookInput{Event: "PostToolUse"}, "working"},
		{"working", hookInput{Event: "Notification", NotificationType: "permission_prompt"}, "waiting_permission"},
		{"working", hookInput{Event: "Notification", Message: "Claude needs your permission to use Bash"}, "waiting_permission"},
		{"idle", hookInput{Event: "Notification", NotificationType: "idle_prompt"}, "waiting_input"},
		{"working", hookInput{Event: "Notification", NotificationType: "auth_success"}, "working"},
		{"working", hookInput{Event: "Stop"}, "idle"},
		{"working", hookInput{Event: "SessionEnd"}, "working"},
		{"exited", hookInput{Event: "UserPromptSubmit"}, "exited"},
	} {
		if got := nextStatus(c.cur, c.in); got != c.out {
			t.Errorf("%s + %+v = %s, want %s", c.cur, c.in, got, c.out)
		}
	}
}

// fakeClaude stands in for claude: it reports how it was started, echoes what it is sent,
// and leaves a child in its process group that ignores the hangup the kernel sends when the
// session leader exits, so only a signal to the whole group stops it.
const fakeClaude = `#!/bin/sh
echo "ARGS:$*"
echo "PWD:$(pwd)"
echo "SESS:$TRUEHAND_SESSION"
echo "LEAK:$(env | grep -c -e '^TRUEHAND_TOKEN' -e '^SECRET_FROM_SERVER')"
(trap '' HUP TERM; exec sleep 300) &
echo "CHILD:$!"
while IFS= read -r line; do
  case "$line" in
    quit) exit 0 ;;
    crash) exit 3 ;;
    size) echo "SIZE:$(stty size)" ;;
    *) echo "GOT:$line" ;;
  esac
done
`

type fixture struct {
	m     *Manager
	store *appstate.Store
	cwd   string
}

func newFixture(t *testing.T) *fixture {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	os.WriteFile(bin, []byte(fakeClaude), 0o755)
	cwd := filepath.Join(dir, "worktree")
	os.MkdirAll(cwd, 0o755)
	store, err := appstate.Open(filepath.Join(dir, "app"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(Config{
		Store: store, Claude: bin, PluginDir: "/p/plugin", Settings: "/a/settings.json", HookAddr: "127.0.0.1:7878",
		AllowCwd:  func(d string) bool { return d == cwd },
		Env:       []string{"PATH=" + os.Getenv("PATH"), "TRUEHAND_TOKEN=tok", "HOME=" + dir},
		KillGrace: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{m: m, store: store, cwd: cwd}
}

// collect reads ch until want shows up in what was read (after history).
func collect(t *testing.T, history []byte, ch <-chan []byte, want string) string {
	t.Helper()
	buf := bytes.NewBuffer(history)
	deadline := time.After(5 * time.Second)
	for !strings.Contains(buf.String(), want) {
		select {
		case b, ok := <-ch:
			if !ok {
				t.Fatalf("stream closed before %q; got %q", want, buf.String())
			}
			buf.Write(b)
		case <-deadline:
			t.Fatalf("timed out waiting for %q; got %q", want, buf.String())
		}
	}
	return buf.String()
}

func field(out, key string) string {
	m := regexp.MustCompile(key + `:([^\r\n]*)`).FindStringSubmatch(out)
	if m == nil {
		return ""
	}
	return m[1]
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// gone waits a little longer than the kill grace for pid to disappear.
func gone(pid int) bool {
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if !alive(pid) {
			return true
		}
	}
	return false
}

func TestPTYSessionLifecycle(t *testing.T) {
	f := newFixture(t)
	if _, err := f.m.Start(StartRequest{Role: RoleDev, Cwd: "/etc", PermissionMode: "default"}); err == nil {
		t.Fatal("a directory that is not a project or worktree must be refused")
	}
	s, err := f.m.Start(StartRequest{Project: "p_1", Task: "T-1", Role: RoleDev, Cwd: f.cwd, PermissionMode: "default", Prompt: DefaultPrompt(RoleDev, "T-1"), Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.m.Proc(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	h1, c1, cancel1 := p.Subscribe()
	out := collect(t, h1, c1, "CHILD:")
	if !strings.Contains(field(out, "ARGS"), "--plugin-dir /p/plugin --settings /a/settings.json --session-id "+s.ClaudeSessionID+" --permission-mode default -- Work on Truehand task T-1") {
		t.Fatalf("args: %q", field(out, "ARGS"))
	}
	if real, _ := filepath.EvalSymlinks(f.cwd); field(out, "PWD") != real && field(out, "PWD") != f.cwd {
		t.Fatalf("pwd: %q", field(out, "PWD"))
	}
	if field(out, "LEAK") != "0" {
		t.Fatal("server credentials reached the agent's environment")
	}
	sid, secret, _, err := ParseSessionEnv(field(out, "SESS"))
	if err != nil || sid != s.ID {
		t.Fatalf("TRUEHAND_SESSION: %q %v", field(out, "SESS"), err)
	}
	child, _ := strconv.Atoi(field(out, "CHILD"))

	// A second viewer sees the same history, then both see live output.
	h2, c2, cancel2 := p.Subscribe()
	defer cancel2()
	if !strings.Contains(string(h2), "CHILD:") {
		t.Fatal("second viewer missed the scrollback")
	}
	p.Write([]byte("hello\n"))
	collect(t, nil, c1, "GOT:hello")
	collect(t, nil, c2, "GOT:hello")
	cancel1()

	if err := p.Resize(80, 24); err != nil {
		t.Fatal(err)
	}
	p.Write([]byte("size\n"))
	collect(t, nil, c2, "SIZE:24 80")

	if err := f.m.Hook(sid, "wrong", []byte(`{"hook_event_name":"UserPromptSubmit"}`)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong secret: %v", err)
	}
	if err := f.m.Hook(sid, secret, []byte(`{"hook_event_name":"Notification","notification_type":"permission_prompt"}`)); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.m.Get(s.ID); got.Status != appstate.StatusWaitingPermission {
		t.Fatalf("status %s", got.Status)
	}
	f.m.Hook(sid, secret, []byte(`{"hook_event_name":"UserPromptSubmit"}`))

	if err := f.m.Signal(s.ID, "term"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not stop")
	}
	if !gone(child) {
		syscall.Kill(child, syscall.SIGKILL)
		t.Fatal("terminating a session must stop its whole process group, even a child that ignores TERM")
	}
	got, _ := f.m.Get(s.ID)
	if got.Status != appstate.StatusExited || got.ExitCode == nil || !got.Resumable || got.Ended == nil || !got.Stopped {
		t.Fatalf("after terminate: %+v", got)
	}
	disk, _ := f.m.Scrollback(s.ID)
	if !strings.Contains(string(disk), "GOT:hello") {
		t.Fatal("scrollback not kept on disk")
	}
	logPath, _ := f.store.ScrollbackPath(s.ID)
	if fi, _ := os.Stat(logPath); fi.Mode().Perm() != 0o600 {
		t.Fatal("scrollback must be private")
	}

	// Resume reuses the claude session id with --resume and no prompt.
	r, err := f.m.Resume(s.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	rp, _ := f.m.Proc(r.ID)
	_, rc, rcancel := rp.Subscribe()
	defer rcancel()
	out = collect(t, nil, rc, "CHILD:")
	if !strings.HasSuffix(strings.TrimSpace(field(out, "ARGS")), "--resume "+s.ClaudeSessionID+" --permission-mode default") {
		t.Fatalf("resume args: %q", field(out, "ARGS"))
	}
	if _, err := f.m.Resume(s.ID, 0, 0); !errors.Is(err, ErrLive) {
		t.Fatalf("resuming a live session: %v", err)
	}
	rp.Write([]byte("crash\n"))
	<-rp.Done()
	if got, _ := f.m.Get(s.ID); got.Status != appstate.StatusCrashed || *got.ExitCode != 3 {
		t.Fatalf("crash: %+v", got)
	}
	if child, _ := strconv.Atoi(field(out, "CHILD")); !gone(child) {
		syscall.Kill(child, syscall.SIGKILL)
		t.Fatal("leftover child survived the session")
	}

	if err := f.m.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatal("delete must remove the scrollback")
	}
	if _, err := f.m.Get(s.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("delete must forget the session")
	}
}

func TestSlowViewerIsDropped(t *testing.T) {
	f := newFixture(t)
	log, _ := os.CreateTemp(t.TempDir(), "log")
	p := &Proc{m: f.m, ring: newRing(1 << 10), log: log, subs: map[chan []byte]struct{}{}}
	_, ch, _ := p.Subscribe()
	for i := 0; i < 600; i++ {
		p.emit([]byte("x"))
	}
	n := 0
	for range ch {
		n++
	}
	if n != 512 {
		t.Fatalf("a viewer that stops reading must be dropped once its buffer fills: got %d", n)
	}
}

func TestScrollbackOnDiskIsBounded(t *testing.T) {
	f := newFixture(t)
	log, _ := os.OpenFile(filepath.Join(t.TempDir(), "log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	p := &Proc{m: f.m, ring: newRing(100), log: log, subs: map[chan []byte]struct{}{}}
	for i := 0; i < 50; i++ {
		p.emit([]byte(strings.Repeat(strconv.Itoa(i%10), 10)))
	}
	fi, _ := log.Stat()
	if fi.Size() > 200 {
		t.Fatalf("scrollback file grew to %d", fi.Size())
	}
	b, _ := os.ReadFile(log.Name())
	if !strings.HasSuffix(string(b), strings.Repeat("9", 10)) {
		t.Fatalf("latest output lost: %q", b)
	}
}

func TestCleanupOrphans(t *testing.T) {
	f := newFixture(t)
	start := func() *exec.Cmd {
		cmd := exec.Command("sleep", "300")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		go cmd.Wait()
		return cmd
	}
	orphan, stranger := start(), start()
	defer stranger.Process.Kill()
	comm, lstart := psInfo(orphan.Process.Pid)
	scomm, _ := psInfo(stranger.Process.Pid)
	f.store.PutSession(appstate.Session{ID: "s_orphan", Status: appstate.StatusWorking, PID: orphan.Process.Pid, Comm: comm, Lstart: lstart, ClaudeSessionID: NewUUID(), Prompted: true})
	// Same pid recorded, but a different start time: the pid was reused, leave it alone.
	f.store.PutSession(appstate.Session{ID: "s_reused", Status: appstate.StatusIdle, PID: stranger.Process.Pid, Comm: scomm, Lstart: "Thu Jan  1 00:00:00 1970", ClaudeSessionID: NewUUID(), Prompted: true})
	f.store.PutSession(appstate.Session{ID: "s_silent", Status: appstate.StatusIdle, ClaudeSessionID: NewUUID()})
	f.store.PutSession(appstate.Session{ID: "s_done", Status: appstate.StatusExited})
	if err := f.m.CleanupOrphans(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for alive(orphan.Process.Pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if alive(orphan.Process.Pid) {
		orphan.Process.Kill()
		t.Fatal("orphan not terminated")
	}
	if !alive(stranger.Process.Pid) {
		t.Fatal("a process that only reused the pid was killed")
	}
	list, _ := f.m.Sessions()
	for _, s := range list {
		if s.Status != appstate.StatusExited {
			t.Errorf("%s still %s", s.ID, s.Status)
		}
		if (s.ID == "s_orphan" || s.ID == "s_reused") != s.Resumable {
			t.Errorf("%s resumable=%v", s.ID, s.Resumable)
		}
	}
}

func TestLeftoversOfAReusedGroupAreSpared(t *testing.T) {
	stranger := exec.Command("sleep", "30")
	stranger.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := stranger.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { stranger.Wait(); close(exited) }()
	defer stranger.Process.Kill()
	// As if our leader's pid had been reused by this new group leader.
	killLeftovers(stranger.Process.Pid)
	select {
	case <-exited:
		t.Fatal("a group led by a live process with that pid is not ours to kill")
	case <-time.After(300 * time.Millisecond):
	}
}

// A session that never got a prompt has no conversation for --resume to find.
func TestSessionWithoutPromptIsNotResumable(t *testing.T) {
	f := newFixture(t)
	s, err := f.m.Start(StartRequest{Role: RoleFree, Cwd: f.cwd, PermissionMode: "default"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := f.m.Proc(s.ID)
	f.m.Signal(s.ID, "term")
	<-p.Done()
	if got, _ := f.m.Get(s.ID); got.Resumable {
		t.Fatalf("resumable without a conversation: %+v", got)
	}
	if _, err := f.m.Resume(s.ID, 0, 0); !errors.Is(err, ErrNothingToResume) {
		t.Fatalf("resume: %v", err)
	}
}
