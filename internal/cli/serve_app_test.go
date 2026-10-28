package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spaghetticoders/truehand/internal/appstate"
	"github.com/spaghetticoders/truehand/internal/humanauth"
)

func TestServeAppStartsWithoutOSAuthAndKeepsOnlyHashes(t *testing.T) {
	s := newSandbox(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Cleanup(func() {
		if p, err := appstate.SocketPath(filepath.Join(home, ".truehand", "app")); err == nil && strings.HasPrefix(p, "/tmp/") {
			os.RemoveAll(filepath.Dir(p))
		}
	})
	t.Setenv("PATH", "/usr/bin:/bin")
	asked := 0
	old := humanauth.Authenticate
	humanauth.Authenticate = func(string) error { asked++; return nil }
	defer func() { humanauth.Authenticate = old }()

	token, shell := strings.Repeat("t", 64), strings.Repeat("s", 64)
	pr, pw := io.Pipe()
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- runIn(s.dir, []string{"serve", "--app", "--addr", "127.0.0.1:0", "--repo", s.dir}, strings.NewReader(`{"token":"`+token+`","shell_secret":"`+shell+`"}`+"\n"), pw, &stderr)
		pw.Close()
	}()
	line, err := bufio.NewReader(pr).ReadString('\n')
	if err != nil {
		t.Fatalf("no ready line: %v\n%s", err, stderr.String())
	}
	go io.Copy(io.Discard, pr)
	var ready struct {
		Ready bool   `json:"ready"`
		URL   string `json:"url"`
		PID   int    `json:"pid"`
	}
	if err := json.Unmarshal([]byte(line), &ready); err != nil || !ready.Ready || ready.PID != os.Getpid() {
		t.Fatalf("ready line %q: %v", line, err)
	}

	me := func(headers map[string]string) (int, map[string]any) {
		req, _ := http.NewRequest(http.MethodGet, ready.URL+"/api/me", nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	if code, _ := me(nil); code != 401 {
		t.Fatalf("no credential: %d", code)
	}
	if code, out := me(map[string]string{"Authorization": "Bearer " + token}); code != 200 || out["canApprove"] != false {
		t.Fatalf("token only: %d %v", code, out)
	}
	if code, out := me(map[string]string{"Authorization": "Bearer " + token, "X-Truehand-Shell": shell}); code != 200 || out["canApprove"] != true {
		t.Fatalf("token and shell secret: %d %v", code, out)
	}
	if asked != 0 {
		t.Fatal("serve must not ask for the OS authentication when it starts")
	}

	appDir := filepath.Join(home, ".truehand", "app")
	if fi, err := os.Stat(appDir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("app dir: %v %v", fi, err)
	}
	filepath.Walk(appDir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		if bytes.Contains(b, []byte(token)) || bytes.Contains(b, []byte(shell)) {
			t.Errorf("%s holds a secret in clear", p)
		}
		return nil
	})
	b, _ := os.ReadFile(filepath.Join(appDir, "state.json"))
	if !strings.Contains(string(b), filepath.Base(s.dir)) {
		t.Fatalf("--repo did not register the project: %s", b)
	}

	if code, out := s.run(`{"token":"`+token+`","shell_secret":"`+shell+`"}`+"\n", "serve", "--app", "--addr", "127.0.0.1:0"); code == 0 || !strings.Contains(out, "already running") {
		t.Fatalf("a second server must be refused by serve.lock: %d %s", code, out)
	}

	req, _ := http.NewRequest(http.MethodPost, ready.URL+"/api/shutdown", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Truehand-Shell", shell)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 204 {
		t.Fatalf("shutdown from the Mac app: %v %v", resp, err)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("serve exited %d: %s", code, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop on /api/shutdown")
	}
}
