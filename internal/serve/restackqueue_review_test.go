package serve

import (
	"encoding/json"
	"testing"

	"github.com/spaghetticoders/truehand/internal/core"
)

// A restack queued from a phone is confirmed only by a person at the Mac: the token that
// queued it (an agent's, possibly) cannot confirm it, and a point that moved since is dropped.
func TestReviewQueuedRestackNeedsTheMac(t *testing.T) {
	e := newEnv(t, false)
	pid, tip := stackRepo(t, e)
	path := "/api/projects/" + pid + "/tasks/COACH-2/restack"
	var v restackView
	e.json("GET", path, "", asToken, &v)
	body, _ := json.Marshal(map[string]string{"point": v.Point, "stackedOn": v.StackedOn})
	var q actResult
	if c := e.json("POST", path, string(body), asToken, &q); c != 202 || q.QueuedID == "" {
		t.Fatalf("queue = %d %+v", c, q)
	}
	stacked := func() bool {
		st, _ := e.s.Store.State()
		pr, _ := st.Project(pid)
		ws, _ := core.Open(pr.Root)
		task, _ := ws.Store.Load("COACH-2")
		return task.StackedOn == "COACH-1" && task.Base == tip
	}
	confirm := "/api/queued/" + q.QueuedID + "/confirm"
	if c := e.code("POST", confirm, "", asToken); c < 400 || stacked() || e.authCount() != 0 {
		t.Fatalf("queued restack confirmed with the token alone = %d (stacked %v, auths %d)", c, stacked(), e.authCount())
	}
	e.authOK = false
	if c := e.code("POST", confirm, "", asShell); c < 400 || stacked() {
		t.Fatalf("queued restack confirmed with the OS dialog cancelled = %d", c)
	}
	e.authOK = true
	if c := e.code("POST", confirm, "", asShell); c != 200 || !stacked() {
		t.Fatalf("queued restack at the Mac = %d (stacked %v)", c, stacked())
	}
}
