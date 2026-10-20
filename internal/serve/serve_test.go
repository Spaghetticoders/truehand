package serve

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/evidence"
)

const goodPlan = "# T\n\n## Goal\ng\n\n## Scope\ns\n\n## Out of scope\no\n\n## Steps\n1. a — verify: b\n\n## Verification\nv\n\n## Risks\nr\n"

func repo(t *testing.T) string {
	dir := filepath.Join(t.TempDir(), "demo")
	os.MkdirAll(dir, 0o755)
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "h@x.y"}, {"config", "user.name", "H"}, {"commit", "-q", "--allow-empty", "-m", "chore: x"}} {
		cmd := exec.Command("git", a...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(string(out))
		}
	}
	os.MkdirAll(filepath.Join(dir, ".truehand"), 0o755)
	os.WriteFile(filepath.Join(dir, ".truehand/config.yaml"), []byte("version: 0\ngates:\n  approved:\n    - check: plan_valid\n    - check: human_approval\n"), 0o644)
	ws, err := core.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ws.NewTask("T-1", "Demo", "")
	os.WriteFile(ws.Store.PlanPath("T-1"), []byte(goodPlan), 0o644)
	os.MkdirAll(filepath.Join(ws.Store.TaskDir("T-1"), "evidence"), 0o755)
	os.WriteFile(filepath.Join(ws.Store.TaskDir("T-1"), "evidence", "x.png"), []byte("png"), 0o644)
	return dir
}

const testToken = "secret"

func get(t *testing.T, url string, out any) int {
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func post(t *testing.T, url, token, body string) int {
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestAPI(t *testing.T) {
	dir := repo(t)
	s := &Server{Repos: []string{dir}, Token: testToken, Approvals: true}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	var repos []repoInfo
	get(t, srv.URL+"/api/repos", &repos)
	if len(repos) != 1 || repos[0].Name != "demo" || repos[0].Counts["planned"] != 1 {
		t.Fatalf("repos: %+v", repos)
	}
	var tasks []map[string]any
	get(t, srv.URL+"/api/repos/demo/tasks", &tasks)
	if len(tasks) != 1 || tasks[0]["id"] != "T-1" || !strings.Contains(toJSON(tasks[0]["missing"]), "human_approval") {
		t.Fatalf("tasks: %+v", tasks)
	}
	var detail map[string]any
	if code := get(t, srv.URL+"/api/repos/demo/tasks/T-1", &detail); code != 200 || detail["view"] == nil {
		t.Fatalf("detail %d %+v", code, detail)
	}
	if code := get(t, srv.URL+"/api/repos/demo/tasks/T-1/files/evidence/x.png", nil); code != 200 {
		t.Fatalf("evidence file: %d", code)
	}
	if code := get(t, srv.URL+"/api/repos/demo/tasks/T-1/files/..%2F..%2Fconfig.yaml", nil); code == 200 {
		t.Fatal("path traversal must be refused")
	}
	if code := get(t, srv.URL+"/api/repos/nope/tasks", nil); code != 404 {
		t.Fatalf("unknown repo: %d", code)
	}

	var pl struct {
		SHA256 string `json:"sha256"`
	}
	get(t, srv.URL+"/api/repos/demo/tasks/T-1/plan", &pl)
	url := srv.URL + "/api/repos/demo/tasks/T-1/approve"
	if code := post(t, url, "", `{"plan_sha256":"`+pl.SHA256+`"}`); code != 401 {
		t.Fatalf("approve without token: %d", code)
	}
	if code := post(t, url, "wrong", `{"plan_sha256":"`+pl.SHA256+`"}`); code != 401 {
		t.Fatalf("approve with wrong token: %d", code)
	}
	if code := post(t, url, "secret", `{"plan_sha256":"stale"}`); code != 409 {
		t.Fatalf("approve with stale plan: %d", code)
	}
	if code := post(t, url, "secret", `{"plan_sha256":"`+pl.SHA256+`"}`); code != 200 {
		t.Fatalf("approve: %d", code)
	}
	ws, _ := core.Open(dir)
	tk, _ := ws.Store.Load("T-1")
	res, _ := ws.Check(tk, board.Approved)
	if !res.Pass {
		t.Fatalf("gate should open after app approval: %+v", res)
	}
	if evidence.HashBytes([]byte(goodPlan)) != pl.SHA256 {
		t.Fatal("plan sha mismatch")
	}
}

func TestEventsStream(t *testing.T) {
	dir := repo(t)
	s := &Server{Repos: []string{dir}, Token: testToken}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	stop := make(chan struct{})
	defer close(stop)
	go s.Watch(stop, 50*time.Millisecond)

	resp, err := http.Get(srv.URL + "/api/events?token=" + testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rd := bufio.NewReader(resp.Body)
	rd.ReadString('\n')
	time.Sleep(200 * time.Millisecond)
	os.WriteFile(filepath.Join(dir, ".truehand/tasks/T-1/plan.md"), []byte(goodPlan+"x"), 0o644)
	done := make(chan string, 1)
	go func() {
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				done <- "ERR " + err.Error()
				return
			}
			if strings.HasPrefix(line, "data: ") {
				done <- strings.TrimSpace(strings.TrimPrefix(line, "data: "))
				return
			}
		}
	}()
	select {
	case got := <-done:
		if got != "demo" {
			t.Fatalf("event data %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no change event")
	}
	_ = io.Discard
}

func toJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestAccessControl(t *testing.T) {
	dir := repo(t)
	s := &Server{Repos: []string{dir}, Token: testToken}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/api/repos")
	if resp.StatusCode != 401 {
		t.Fatalf("GET without token: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/repos", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Origin", "https://evil.example")
	resp, _ = http.DefaultClient.Do(req)
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("no CORS headers may be sent")
	}
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/repos", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Host = "evil.example"
	if resp, _ = http.DefaultClient.Do(req); resp.StatusCode != 403 {
		t.Fatalf("foreign Host (DNS rebinding) must be refused: %d", resp.StatusCode)
	}

	// Symlinks planted in a task directory are not followed.
	td := filepath.Join(dir, ".truehand/tasks/T-1")
	os.Symlink("/etc/hosts", filepath.Join(td, "context.md"))
	if code := get(t, srv.URL+"/api/repos/demo/tasks/T-1/files/context.md", nil); code == 200 {
		t.Fatal("symlinked file must not be served")
	}
	os.Remove(filepath.Join(td, "plan.md"))
	os.Symlink("/etc/hosts", filepath.Join(td, "plan.md"))
	var pl map[string]any
	get(t, srv.URL+"/api/repos/demo/tasks/T-1/plan", &pl)
	if pl["exists"] != false {
		t.Fatalf("symlinked plan must not be read: %v", pl)
	}

	// Without the startup authentication, the API refuses approvals.
	if code := post(t, srv.URL+"/api/repos/demo/tasks/T-1/approve", testToken, `{"plan_sha256":"x"}`); code != 403 {
		t.Fatalf("approve with approvals disabled: %d", code)
	}
}
