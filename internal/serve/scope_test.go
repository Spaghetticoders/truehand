package serve

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/core"
)

// scopedTask gives T-1 the scope lib/** and changes lib/a.txt and two files outside it.
func scopedTask(t *testing.T, e *env) {
	ws, _ := core.Open(e.root)
	t1, _ := ws.Store.Load("T-1")
	t1.Scope = []string{"lib/**"}
	ws.Store.Save(t1)
	for _, f := range []string{"lib/a.txt", "app/view.dart", "app/lock.dart"} {
		os.MkdirAll(filepath.Join(e.root, filepath.Dir(f)), 0o755)
		os.WriteFile(filepath.Join(e.root, f), []byte("x\n"), 0o644)
	}
}

func lastEvent(t *testing.T, e *env, typ string) board.Event {
	t.Helper()
	ws, _ := core.Open(e.root)
	events, _ := ws.Store.Events("T-1")
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == typ {
			return events[i]
		}
	}
	t.Fatalf("no %s event", typ)
	return board.Event{}
}

func TestWidenScopeIsAHumanAct(t *testing.T) {
	e := newEnv(t, false)
	scopedTask(t, e)
	path := e.taskPath("T-1") + "/scope"
	var v scopeView
	if c := e.json("GET", path, "", asToken, &v); c != 200 || strings.Join(v.Outside, ",") != "app/lock.dart,app/view.dart" || v.OutsideCount != 2 || len(v.Add) != 2 {
		t.Fatalf("preview = %d %+v", c, v)
	}
	scope := func() []string {
		ws, _ := core.Open(e.root)
		t1, _ := ws.Store.Load("T-1")
		return t1.Scope
	}
	// A glob that is not among the files outside: refused before anyone is asked.
	resp, b := e.req("POST", path, `{"add":["**"]}`, asShell)
	if resp.StatusCode != 409 || !strings.Contains(string(b), "scope-moved") || e.authCount() != 0 {
		t.Fatalf("add ** = %d %s", resp.StatusCode, b)
	}
	// The token alone waits in the queue.
	var q actResult
	if c := e.json("POST", path, `{"add":["app/view.dart"]}`, asToken, &q); c != 202 || slices.Contains(scope(), "app/view.dart") {
		t.Fatalf("widen from the token = %d %+v", c, q)
	}
	// At the Mac: the dialog names the files, the event the person.
	var out struct {
		OK    bool     `json:"ok"`
		Scope []string `json:"scope"`
	}
	if c := e.json("POST", path, `{"add":["app/view.dart"]}`, asShell, &out); c != 200 || !slices.Contains(out.Scope, "app/view.dart") || slices.Contains(out.Scope, "app/lock.dart") {
		t.Fatalf("widen at the Mac = %d %+v", c, out)
	}
	if !strings.Contains(e.auths[0], "app/view.dart") || !strings.Contains(e.auths[0], "T-1") {
		t.Fatalf("dialog = %q", e.auths[0])
	}
	if ev := lastEvent(t, e, "scope-widened"); ev.Actor != "human:h@x.y" || ev.Detail["via"] != "touchid" {
		t.Fatalf("event = %+v", ev)
	}
	// The queued one is confirmed only while still outside: view.dart is in scope now.
	auths := e.authCount()
	if resp, b := e.req("POST", "/api/queued/"+q.QueuedID+"/confirm", "", asShell); resp.StatusCode != 409 || !strings.Contains(string(b), "scope-moved") || e.authCount() != auths {
		t.Fatalf("stale queued widening = %d %s", resp.StatusCode, b)
	}
}

// An owner's passkey signs the body: a confirmation for one file does not add another.
func TestWidenScopeOwnerSignsTheGlobs(t *testing.T) {
	e := newEnv(t, false)
	a := e.headless()
	s25 := e.phone(false)
	e.bootstrap(a, s25)
	scopedTask(t, e)
	path := e.taskPath("T-1") + "/scope"
	resp, b := e.req("POST", path, `{"add":["app/view.dart"]}`, asToken)
	if resp.StatusCode != 409 || !strings.Contains(string(b), codeOwnerNeeded) {
		t.Fatalf("headless without owner = %d %s", resp.StatusCode, b)
	}
	signedFor := `{"add":["app/view.dart"]}`
	_, h := s25.signed("owner", map[string]string{"method": "POST", "path": path, "bodySha256": bodySHA(signedFor)})
	if c := s25.json("POST", path, `{"add":["app/lock.dart"]}`, map[string]string{ownerHeader: h}, nil); c != 403 {
		t.Fatalf("signature for view.dart used for lock.dart = %d", c)
	}
	var out struct {
		Scope []string `json:"scope"`
	}
	if c := s25.asOwner("POST", path, signedFor, &out); c != 200 || !slices.Contains(out.Scope, "app/view.dart") {
		t.Fatalf("owner widening = %d %+v", c, out)
	}
	if ev := lastEvent(t, e, "scope-widened"); ev.Detail["via"] != "passkey" {
		t.Fatalf("event = %+v", ev)
	}
}

// A file whose name looks like a glob is proposed escaped (it matches only itself), and the
// dialog shows its name as it is.
func TestWidenScopeWithAGlobLikeName(t *testing.T) {
	e := newEnv(t, false)
	scopedTask(t, e)
	os.MkdirAll(filepath.Join(e.root, "docs"), 0o755)
	os.WriteFile(filepath.Join(e.root, "docs", "*"), []byte("x\n"), 0o644)
	var v scopeView
	e.json("GET", e.taskPath("T-1")+"/scope", "", asToken, &v)
	i := slices.Index(v.Outside, "docs/*")
	if i < 0 || v.Add[i] != `docs/\*` {
		t.Fatalf("preview = %+v", v)
	}
	b, _ := json.Marshal(map[string][]string{"add": {v.Add[i]}})
	var out struct {
		Scope []string `json:"scope"`
	}
	if c := e.json("POST", e.taskPath("T-1")+"/scope", string(b), asShell, &out); c != 200 || !slices.Contains(out.Scope, `docs/\*`) || slices.Contains(out.Scope, "docs/*") {
		t.Fatalf("widen = %d %+v", c, out)
	}
	if !strings.Contains(e.auths[0], "docs/*") || strings.Contains(e.auths[0], `\*`) {
		t.Fatalf("dialog = %q", e.auths[0])
	}
}
