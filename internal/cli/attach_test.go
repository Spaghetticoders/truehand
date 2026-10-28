package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/machines"
	"github.com/spaghetticoders/truehand/internal/serve"
)

const fakeClaudeCLI = `#!/bin/sh
echo "ARGS:$*"
while IFS= read -r line; do echo "GOT:$line"; done
`

// slowReader hands out its chunks one by one, waiting before each, like a person typing.
// before[i], when set, holds chunk i until it is true (for at most 10 s).
type slowReader struct {
	chunks []string
	wait   time.Duration
	before map[int]func() bool
	n      int
}

func (r *slowReader) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		time.Sleep(time.Hour)
		return 0, io.EOF
	}
	time.Sleep(r.wait)
	if cond := r.before[r.n]; cond != nil {
		for deadline := time.Now().Add(10 * time.Second); !cond() && time.Now().Before(deadline); {
			time.Sleep(50 * time.Millisecond)
		}
	}
	r.n++
	n := copy(p, r.chunks[0])
	r.chunks = r.chunks[1:]
	return n, nil
}

// startLocalServe runs `serve` in this process with a short home (the socket path must fit)
// and a fake claude, and stops it at the end.
func startLocalServe(t *testing.T, s *sandbox) string {
	home, err := os.MkdirTemp("/tmp", "thc")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, "bin")
	os.MkdirAll(bin, 0o755)
	os.WriteFile(filepath.Join(bin, "claude"), []byte(fakeClaudeCLI), 0o755)
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	done := make(chan int, 1)
	var out bytes.Buffer
	go func() {
		done <- runIn(s.dir, []string{"serve", "--addr", "127.0.0.1:0", "--repo", s.dir}, strings.NewReader(""), &out, &out)
	}()
	sock := filepath.Join(home, ".truehand", "app", "serve.sock")
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Cleanup(func() {
		client, _ := socketClient()
		if resp, err := client.Post("http://socket/api/shutdown", "", nil); err == nil {
			resp.Body.Close()
		}
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("serve did not stop")
		}
		os.RemoveAll(home)
	})
	return home
}

func TestSessionsAndAttach(t *testing.T) {
	// These tests may run inside an agent session; here the terminal stands for a person.
	old := serve.AgentProcess
	serve.AgentProcess = func(int) bool { return false }
	t.Cleanup(func() { serve.AgentProcess = old })
	s := newSandbox(t)
	s.must("init", "--stack", "generic")
	startLocalServe(t, s)
	client, err := socketClient()
	if err != nil {
		t.Fatal(err)
	}
	var snap struct {
		Projects []struct{ ID string } `json:"projects"`
	}
	resp, err := client.Get("http://socket/api/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	json.NewDecoder(resp.Body).Decode(&snap)
	resp.Body.Close()
	if len(snap.Projects) != 1 {
		t.Fatalf("projects: %+v", snap)
	}
	resp, err = client.Post("http://socket/api/projects/"+snap.Projects[0].ID+"/sessions", "application/json", strings.NewReader(`{}`))
	if err != nil || resp.StatusCode != 201 {
		t.Fatalf("session over the socket: %v %v", resp, err)
	}
	var sess struct{ ID string }
	json.NewDecoder(resp.Body).Decode(&sess)
	resp.Body.Close()

	out := s.must("sessions")
	if !strings.Contains(out, sess.ID) || !strings.Contains(out, "free") {
		t.Fatalf("sessions:\n%s", out)
	}

	var stdout lockedWriter
	var stderr bytes.Buffer
	// Detach only once the echo is back: a fixed delay is too short on a busy machine.
	in := &slowReader{chunks: []string{"hello\r", "bye\r\x1d"}, wait: 400 * time.Millisecond,
		before: map[int]func() bool{1: func() bool { return strings.Contains(stdout.String(), "GOT:hello") }}}
	code := runIn(s.dir, []string{"attach", sess.ID[:6]}, in, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "GOT:hello") || !strings.Contains(stderr.String(), "detached") {
		t.Fatalf("attach: %d\nstdout %q\nstderr %q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(s.must("sessions"), sess.ID) {
		t.Fatal("detaching must leave the agent running")
	}

	stdout = lockedWriter{}
	stderr.Reset()
	in = &slowReader{chunks: []string{"typed by a viewer\r", "\x1d"}, wait: 300 * time.Millisecond}
	runIn(s.dir, []string{"attach", "--view", sess.ID}, in, &stdout, &stderr)
	if strings.Contains(stdout.String(), "GOT:typed by a viewer") {
		t.Fatal("--view must not type into the session")
	}

	oldDetector := core.AgentDetector
	core.AgentDetector = func() int { return 4242 }
	defer func() { core.AgentDetector = oldDetector }()
	if code, out := s.run("", "attach", sess.ID); code == 0 || !strings.Contains(out, "agent session") {
		t.Fatalf("attach inside an agent: %d %s", code, out)
	}
}

func TestMachineCommands(t *testing.T) {
	s := newSandbox(t)
	home, err := os.MkdirTemp("/tmp", "thm")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	t.Setenv("HOME", filepath.Join(home, "mac"))
	peerHome := filepath.Join(home, "peer")
	tools := filepath.Join(home, "tools")
	for _, d := range []string{peerHome, tools} {
		os.MkdirAll(d, 0o755)
	}
	os.WriteFile(filepath.Join(tools, "ssh"), []byte(`#!/bin/sh
while [ $# -gt 0 ]; do case "$1" in -o) shift 2 ;; -*) shift ;; *) break ;; esac; done
[ "$1" = down.example ] && { echo "ssh: connect to host down.example: Connection refused" >&2; exit 255; }
shift
exec env -i HOME="$PEER_HOME" SHELL=/bin/sh PATH=/usr/bin:/bin /bin/sh -c "$1"
`), 0o755)
	t.Setenv("PEER_HOME", peerHome)
	fakeBin := filepath.Join(tools, "truehand")
	os.WriteFile(fakeBin, []byte("#!/bin/sh\n[ \"$1\" = version ] && echo old\n[ \"$1\" = peer-proxy ] && exit 3\nexit 0\n"), 0o755)
	oldSSH, oldBin := sshFor, binaryFor
	sshFor = func() (*machines.SSH, error) { return &machines.SSH{Bin: filepath.Join(tools, "ssh")}, nil }
	binaryFor = func(string, string) (string, error) { return fakeBin, nil }
	defer func() { sshFor, binaryFor = oldSSH, oldBin }()

	if code, out := s.run("", "machine", "add", "down.example"); code == 0 || !strings.Contains(out, "refused") {
		t.Fatalf("unreachable machine: %d %s", code, out)
	}
	// The fake binary never answers as a peer: the machine is installed but not kept.
	if code, out := s.run("", "machine", "add", "mini.example", "--label", "Mac mini"); code == 0 {
		t.Fatalf("a peer that never answers must fail: %s", out)
	}
	if _, err := os.Stat(filepath.Join(peerHome, ".truehand", "bin", "truehand")); err != nil {
		t.Fatal("truehand must be installed in the remote ~/.truehand/bin")
	}
	if out := s.must("machine", "list"); !strings.Contains(out, "no machines") {
		t.Fatalf("failed machines are not kept:\n%s", out)
	}
	old := core.AgentDetector
	core.AgentDetector = func() int { return 4242 }
	defer func() { core.AgentDetector = old }()
	if code, out := s.run("", "machine", "add", "mini.example"); code == 0 || !strings.Contains(out, "agent session") {
		t.Fatalf("machine add inside an agent: %d %s", code, out)
	}
}

// lockedWriter is a buffer written by attach and read by the test at the same time.
type lockedWriter struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *lockedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}
