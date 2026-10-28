package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// The socket is reachable by every process of this user, the agents included: it must not
// let an agent do what only a person may start, nor type into a terminal.
func TestReviewSocketGivesAgentsNoHumanPowers(t *testing.T) {
	e := newEnv(t, false)
	sock := httptest.NewServer(e.s.SocketHandler())
	defer sock.Close()
	post := func(path, body string) int {
		req, _ := http.NewRequest("POST", sock.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post(e.taskPath("T-1")+"/actions/start-review", ""); code < 300 {
		t.Errorf("an agent on the socket started a review session of its own task: %d", code)
	}
	if code := post("/api/projects/"+e.pid+"/sessions", `{"prompt":"x"}`); code < 300 {
		t.Errorf("an agent on the socket started an agent session: %d", code)
	}

	var sess struct {
		ID string `json:"id"`
	}
	if code := e.json("POST", "/api/projects/"+e.pid+"/sessions", `{}`, asToken, &sess); code != 201 && code != 409 {
		t.Fatalf("session: %d", code)
	}
	if sess.ID == "" {
		list, _ := e.s.Agents.Sessions()
		for _, x := range list {
			sess.ID = x.ID
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(sock.URL, "http")+"/api/sessions/"+sess.ID+"/pty", nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.Write(ctx, websocket.MessageText, []byte(`{"t":"in","d":"typed-by-agent\r"}`))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := e.s.Agents.Scrollback(sess.ID)
		if strings.Contains(string(b), "GOT:typed-by-agent") {
			t.Fatal("an agent on the socket typed into a session's terminal (it can answer its own permission prompts)")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
