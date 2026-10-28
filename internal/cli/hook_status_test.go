package cli

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHookStatusForwardsSilentlyAndNeverFails(t *testing.T) {
	var gotPath, gotSession, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotSession = r.URL.Path, r.Header.Get("X-Truehand-Session")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"decision":"block","reason":"must never reach the agent"}`))
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	run := func(env, stdin string) (int, string) {
		t.Setenv("TRUEHAND_SESSION", env)
		var out bytes.Buffer
		code := runIn(t.TempDir(), []string{"hook", "status"}, strings.NewReader(stdin), &out, &out)
		return code, out.String()
	}

	code, out := run("s_1:k@"+addr, `{"hook_event_name":"Stop"}`)
	if code != 0 || out != "" {
		t.Fatalf("exit %d, output %q", code, out)
	}
	if gotPath != "/hook/status" || gotSession != "s_1:k" || gotBody != `{"hook_event_name":"Stop"}` {
		t.Fatalf("forwarded %q %q %q", gotPath, gotSession, gotBody)
	}

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	ln.Close()
	for _, env := range []string{"", "garbage", "s_1:k@" + dead, "s_1:k@example.com:80"} {
		if code, out := run(env, "{}"); code != 0 || out != "" {
			t.Errorf("TRUEHAND_SESSION=%q: exit %d, output %q", env, code, out)
		}
	}
}
