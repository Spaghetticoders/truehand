package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spaghetticoders/truehand/internal/appstate"
	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/evidence"
	"github.com/spaghetticoders/truehand/internal/hands"
)

// stackRepo: COACH-1 reviewed on feat with passing evidence; COACH-2 started from main with
// a commit of its own. It returns the project id and the commit that closes COACH-1.
func stackRepo(t *testing.T, e *env) (string, string) { return stackRepoDirty(t, e, false) }

// stackRepoDirty: with dirty, COACH-1's hand ran with notes.txt on disk, never committed.
func stackRepoDirty(t *testing.T, e *env, dirty bool) (string, string) {
	dir := filepath.Join(t.TempDir(), "stack")
	os.MkdirAll(filepath.Join(dir, ".truehand"), 0o755)
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "config", "user.email", "h@x.y")
	git(t, dir, "config", "user.name", "H")
	os.WriteFile(filepath.Join(dir, ".truehand/config.yaml"), []byte("version: 0\nbase: main\ncommands:\n  review: \"true\"\ngates:\n  verified:\n    - check: hand_passed\n      with: { hand: review }\n"), 0o644)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "chore: truehand")
	main := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
	git(t, dir, "checkout", "-q", "-b", "feat")
	ws, err := core.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	c1, _ := ws.NewTask("COACH-1", "first", "")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "feat: one")
	if dirty {
		os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("someone else's\n"), 0o644)
	}
	sess, err := hands.Begin(ws, c1, "review", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Finish(evidence.Pass, "ok", ""); err != nil {
		t.Fatal(err)
	}
	if dirty {
		os.Remove(filepath.Join(dir, "notes.txt"))
	}
	c1.State = board.Reviewed
	ws.Store.Save(c1)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "chore: reviewed")
	tip := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
	ws.Store.Create(&board.Task{ID: "COACH-2", Title: "second", State: board.Building, Base: main, BaseRef: "main"})
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("two\n"), 0o644)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "feat: two")
	p, err := e.s.Store.AddProject(dir, "Acme", "")
	if err != nil {
		t.Fatal(err)
	}
	return p.ID, tip
}

func TestRestackIsAHumanAct(t *testing.T) {
	e := newEnv(t, false)
	pid, tip := stackRepo(t, e)
	path := "/api/projects/" + pid + "/tasks/COACH-2/restack"
	var v restackView
	if c := e.json("GET", path, "", asToken, &v); c != 200 || v.Point != tip || v.StackedOn != "COACH-1" || v.PredState != "reviewed" || len(v.Covered) != 1 || len(v.Skipped) < 2 {
		t.Fatalf("preview = %d %+v", c, v)
	}
	body, _ := json.Marshal(map[string]string{"point": v.Point, "stackedOn": v.StackedOn})
	stacked := func() bool {
		st, _ := e.s.Store.State()
		pr, _ := st.Project(pid)
		ws, _ := core.Open(pr.Root)
		task, _ := ws.Store.Load("COACH-2")
		return task.StackedOn == "COACH-1" && task.Base == tip
	}
	// Another point than the one shown: refused before anyone is asked.
	wrong, _ := json.Marshal(map[string]string{"point": strings.Repeat("a", 40), "stackedOn": "COACH-1"})
	resp, b := e.req("POST", path, string(wrong), asShell)
	if resp.StatusCode != 409 || !strings.Contains(string(b), "restack-moved") || e.authCount() != 0 {
		t.Fatalf("wrong point = %d %s", resp.StatusCode, b)
	}
	// The token alone (a phone without a passkey) waits in the queue; nothing moves.
	var q actResult
	if c := e.json("POST", path, string(body), asToken, &q); c != 202 || !q.Queued || stacked() {
		t.Fatalf("restack from the token = %d %+v", c, q)
	}
	// The terminal's socket (agents included) never restacks.
	sock := httptest.NewServer(e.s.SocketHandler())
	defer sock.Close()
	req, _ := http.NewRequest("POST", sock.URL+path, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	if r, err := http.DefaultClient.Do(req); err != nil || r.StatusCode < 400 {
		t.Fatalf("restack over the socket = %v %v", r, err)
	}
	if e.authCount() != 0 || stacked() {
		t.Fatal("restacked without a person")
	}
	// At the Mac, with the OS dialog naming task, predecessor and point.
	if c := e.code("POST", path, string(body), asShell); c != 200 || !stacked() || e.authCount() != 1 {
		t.Fatalf("restack at the Mac = %d (stacked %v)", c, stacked())
	}
	if last := e.auths[0]; !strings.Contains(last, "COACH-2") || !strings.Contains(last, "COACH-1") || !strings.Contains(last, tip[:12]) {
		t.Fatalf("dialog = %q", last)
	}
	ev := lastRestacked(t, e, pid)
	if ev.Actor != "human:h@x.y" || ev.Detail["via"] != "touchid" || ev.Detail["base"] != tip || ev.Detail["stacked_on"] != "COACH-1" {
		t.Fatalf("restacked event = %+v", ev)
	}
}

func lastRestacked(t *testing.T, e *env, pid string) board.Event {
	t.Helper()
	st, _ := e.s.Store.State()
	pr, _ := st.Project(pid)
	ws, _ := core.Open(pr.Root)
	events, _ := ws.Store.Events("COACH-2")
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == "restacked" {
			return events[i]
		}
	}
	t.Fatal("no restacked event")
	return board.Event{}
}

// From the queue: the person at the Mac is the author, the phone the one who asked; a point
// changed in the queued request (the file is the agents' to write) is refused.
func TestQueuedRestackAuthorAndPoint(t *testing.T) {
	e := newEnv(t, false)
	pid, tip := stackRepo(t, e)
	path := "/api/projects/" + pid + "/tasks/COACH-2/restack"
	var v restackView
	e.json("GET", path, "", asToken, &v)
	body, _ := json.Marshal(map[string]string{"point": v.Point, "stackedOn": v.StackedOn})
	var q actResult
	if c := e.json("POST", path, string(body), asToken, &q); c != 202 {
		t.Fatalf("queue = %d", c)
	}
	// An agent rewrites the queued point.
	e.s.Store.UpdateAuth(func(a *appstate.Auth) error {
		for i := range a.Queued {
			if a.Queued[i].ID == q.QueuedID {
				a.Queued[i].Params["point"] = strings.Repeat("a", 40)
			}
		}
		return nil
	})
	resp, b := e.req("POST", "/api/queued/"+q.QueuedID+"/confirm", "", asShell)
	if resp.StatusCode != 409 || !strings.Contains(string(b), "restack-moved") || e.authCount() != 0 {
		t.Fatalf("confirm with a changed point = %d %s", resp.StatusCode, b)
	}
	e.json("POST", path, string(body), asToken, &q)
	if c := e.code("POST", "/api/queued/"+q.QueuedID+"/confirm", "", asShell); c != 200 {
		t.Fatalf("confirm = %d", c)
	}
	ev := lastRestacked(t, e, pid)
	if ev.Actor != "human:h@x.y" || ev.Detail["via"] != "queue" || ev.Detail["base"] != tip || ev.Detail["requested_from"] == nil {
		t.Fatalf("restacked event = %+v", ev)
	}
}

func TestRestackHeadlessNeedsAnOwner(t *testing.T) {
	e := newEnv(t, false)
	a := e.headless()
	pid, _ := stackRepo(t, e)
	path := "/api/projects/" + pid + "/tasks/COACH-2/restack"
	var v restackView
	e.json("GET", path, "", asToken, &v)
	body, _ := json.Marshal(map[string]string{"point": v.Point, "stackedOn": v.StackedOn})
	resp, b := e.req("POST", path, string(body), asToken)
	if resp.StatusCode != 409 || !strings.Contains(string(b), codeOwnerNeeded) {
		t.Fatalf("headless restack without owner = %d %s", resp.StatusCode, b)
	}
	s25 := e.phone(false)
	e.bootstrap(a, s25)
	if c := s25.asOwner("POST", path, string(body), nil); c != 200 {
		t.Fatalf("restack with the owner's passkey = %d", c)
	}
	if ev := lastRestacked(t, e, pid); ev.Actor != "human:h@x.y" || ev.Detail["via"] != "passkey" {
		t.Fatalf("restacked event = %+v", ev)
	}
}

// A stacked task whose chain broke is shown with why, not an error page; the task it stacks
// on shows the point it is judged at and who builds on it.
func TestBoardShowsStacks(t *testing.T) {
	e := newEnv(t, false)
	pid, tip := stackRepo(t, e)
	st, _ := e.s.Store.State()
	pr, _ := st.Project(pid)
	ws, _ := core.Open(pr.Root)
	if _, err := ws.Restack("COACH-2", tip, "COACH-1"); err != nil {
		t.Fatal(err)
	}
	find := func() (c1, c2 taskSummary) {
		var snap snapshotView
		e.json("GET", "/api/snapshot", "", asToken, &snap)
		for _, x := range snap.Tasks {
			switch x.ID {
			case "COACH-1":
				c1 = x
			case "COACH-2":
				c2 = x
			}
		}
		return
	}
	c1, c2 := find()
	if c2.StackedOn != "COACH-1" || c2.StackError != "" || c1.Tip != tip || len(c1.StackedBy) != 1 || c1.StackedBy[0] != "COACH-2" {
		t.Fatalf("stack on the board: %+v / %+v", c1, c2)
	}
	// COACH-1 goes back to building: COACH-2's chain no longer holds.
	t1, _ := ws.Store.Load("COACH-1")
	t1.State = board.Building
	ws.Store.Save(t1)
	e.s.forget(pid)
	if _, c2 = find(); !strings.Contains(c2.StackError, "restack") {
		t.Fatalf("broken stack on the board: %+v", c2)
	}
	var d struct {
		taskSummary
		Description string `json:"description"`
	}
	if c := e.json("GET", "/api/projects/"+pid+"/tasks/COACH-2", "", asToken, &d); c != 200 || !strings.Contains(d.StackError, "restack") || d.StackedOn != "COACH-1" {
		t.Fatalf("task page of a broken stack = %d %+v", c, d.taskSummary)
	}
}

// The predecessor's evidence saw a file that is not in its commits: the person sees the list
// and confirms exactly it; the event records it.
func TestRestackWithIgnoredFiles(t *testing.T) {
	e := newEnv(t, false)
	pid, tip := stackRepoDirty(t, e, true)
	path := "/api/projects/" + pid + "/tasks/COACH-2/restack"
	var v restackView
	if c := e.json("GET", path, "", asToken, &v); c != 200 || v.Point != tip || v.IgnoreCount != 1 || v.Ignore[0] != "notes.txt" || v.IgnoreHash != core.IgnoreHash([]string{"notes.txt"}) {
		t.Fatalf("preview = %d %+v", c, v)
	}
	for _, h := range []string{"", strings.Repeat("0", 64)} {
		b, _ := json.Marshal(map[string]string{"point": v.Point, "stackedOn": v.StackedOn, "ignoreHash": h})
		if resp, out := e.req("POST", path, string(b), asShell); resp.StatusCode != 409 || !strings.Contains(string(out), "restack-moved") || e.authCount() != 0 {
			t.Fatalf("ignoreHash %q = %d %s", h, resp.StatusCode, out)
		}
	}
	b, _ := json.Marshal(map[string]string{"point": v.Point, "stackedOn": v.StackedOn, "ignoreHash": v.IgnoreHash})
	if c := e.code("POST", path, string(b), asShell); c != 200 {
		t.Fatalf("restack with the list = %d", c)
	}
	if !strings.Contains(e.auths[0], "1 file(s)") {
		t.Fatalf("dialog = %q", e.auths[0])
	}
	ev := lastRestacked(t, e, pid)
	list, _ := ev.Detail["ignore"].([]any)
	if ev.Actor != "human:h@x.y" || ev.Detail["ignore_hash"] != v.IgnoreHash || len(list) != 1 || list[0] != "notes.txt" {
		t.Fatalf("restacked event = %+v", ev)
	}
}
