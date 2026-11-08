package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/spaghetticoders/truehand/internal/appstate"
	"github.com/spaghetticoders/truehand/internal/serve"
)

// serve --app with the Keychain's list on stdin starts trusting owners at once: the list it
// read reaches the server, so passkeys are not waiting for a later request from the app.
func TestReviewAppStdinRevocationsReachTheServer(t *testing.T) {
	s := newSandbox(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")
	oldAgent := serve.AgentProcess
	serve.AgentProcess = func(int) bool { return false }
	defer func() { serve.AgentProcess = oldAgent }()
	tok, sec := strings.Repeat("a", 64), strings.Repeat("b", 64)
	pr, pw := io.Pipe()
	done := make(chan int, 1)
	go func() {
		done <- runIn(s.dir, []string{"serve", "--app", "--addr", "127.0.0.1:0"}, strings.NewReader(`{"token":"`+tok+`","shell_secret":"`+sec+`","revoked":[]}`+"\n"), pw, io.Discard)
		pw.Close()
	}()
	line, err := bufio.NewReader(pr).ReadString('\n')
	if err != nil {
		t.Fatalf("no start line: %v", err)
	}
	go io.Copy(io.Discard, pr)
	addr := regexp.MustCompile(`http://(127\.0\.0\.1:\d+)`).FindStringSubmatch(line)
	if addr == nil {
		t.Fatalf("no address in %q", line)
	}
	req, _ := http.NewRequest("GET", "http://"+addr[1]+"/api/passkeys", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("GET /api/passkeys: %v %v", resp, err)
	}
	var v map[string]any
	json.NewDecoder(resp.Body).Decode(&v)
	resp.Body.Close()
	if e, ok := v["revocationsError"]; ok {
		t.Errorf("the list sent on stdin did not reach the server: %v", e)
	}

	sock, _ := appstate.SocketPath(filepath.Join(home, ".truehand", "app"))
	c := http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	sreq, _ := http.NewRequest("POST", "http://truehand/api/shutdown", nil)
	sreq.Header.Set("Authorization", "Bearer "+tok)
	sreq.Header.Set("X-Truehand-Shell", sec)
	c.Do(sreq)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop")
	}
}
