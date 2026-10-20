package tickets

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLinearFetch(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var body struct{ Variables map[string]string }
		json.NewDecoder(r.Body).Decode(&body)
		if body.Variables["id"] != "SPA-7" {
			w.Write([]byte(`{"data":{"issue":null}}`))
			return
		}
		w.Write([]byte(`{"data":{"issue":{"identifier":"SPA-7","title":"Login","url":"https://linear.app/x/SPA-7","description":"Build login","state":{"name":"Todo"},"assignee":{"name":"Fra"},"labels":{"nodes":[{"name":"ui"}]},
		"comments":{"nodes":[{"body":"second","createdAt":"2026-10-02T10:00:00Z","user":{"name":"B"}},{"body":"use node 12:34","createdAt":"2026-10-01T10:00:00Z","user":{"name":"A"}}]},
		"attachments":{"nodes":[{"title":"Figma","url":"https://figma.com/x"}]},"parent":null,"children":{"nodes":[]},"relations":{"nodes":[]}}}}`))
	}))
	defer srv.Close()
	l := &Linear{Key: "lin_api_x", Endpoint: srv.URL}
	tk, err := l.Fetch(context.Background(), "SPA-7")
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "lin_api_x" {
		t.Fatalf("auth header %q: Linear personal keys go raw, no Bearer", gotAuth)
	}
	if tk.Comments[0].Body != "use node 12:34" {
		t.Fatalf("comments must be oldest first: %+v", tk.Comments)
	}
	md := Markdown(tk)
	for _, want := range []string{"# SPA-7 — Login", "Build login", "## Comments (2)", "use node 12:34", "[Figma](https://figma.com/x)", "Labels: ui"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q:\n%s", want, md)
		}
	}
	if _, err := l.Fetch(context.Background(), "SPA-404"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestLinearErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"errors":[{"message":"Authentication required"}]}`))
	}))
	defer srv.Close()
	_, err := (&Linear{Key: "bad", Endpoint: srv.URL}).Fetch(context.Background(), "SPA-1")
	if err == nil || !strings.Contains(err.Error(), "Authentication required") {
		t.Fatalf("expected auth error, got %v", err)
	}
}

func TestParseRef(t *testing.T) {
	for ref, want := range map[string][2]string{
		"linear:SPA-1":  {"linear", "SPA-1"},
		"SPA-1":         {"linear", "SPA-1"},
		"github:o/r#3":  {"github", "o/r#3"},
		"file:brief.md": {"file", "brief.md"},
		"https://x.y/z": {"linear", "https://x.y/z"},
	} {
		a, id := ParseRef(ref, "linear")
		if a != want[0] || id != want[1] {
			t.Errorf("ParseRef(%q) = %q,%q want %v", ref, a, id, want)
		}
	}
}
