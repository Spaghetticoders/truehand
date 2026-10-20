// Package serve is the local HTTP API behind the Truehand app: read-only views of the board
// across repos, evidence files, a change stream (SSE), and human approval guarded by a
// token that only the person who started the server sees.
package serve

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/evidence"
	"github.com/spaghetticoders/truehand/internal/humanauth"
	"github.com/spaghetticoders/truehand/internal/plan"
)

type Server struct {
	Repos []string // repo roots
	Token string   // required on every API call
	// Approvals is set only after a human authenticated with the OS when the server started.
	Approvals bool
	// AnyHost accepts any Host header (listening on all interfaces). Otherwise only loopback
	// names are accepted, which defeats DNS rebinding from web pages.
	AnyHost bool
	Web     fs.FS // optional static app

	mu      sync.Mutex
	clients map[chan string]bool
}

func NewToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/repos", s.repos)
	mux.HandleFunc("GET /api/repos/{repo}/tasks", s.tasks)
	mux.HandleFunc("GET /api/repos/{repo}/tasks/{id}", s.task)
	mux.HandleFunc("GET /api/repos/{repo}/tasks/{id}/plan", s.plan)
	mux.HandleFunc("GET /api/repos/{repo}/tasks/{id}/files/{path...}", s.file)
	mux.HandleFunc("POST /api/repos/{repo}/tasks/{id}/approve", s.approve)
	mux.HandleFunc("GET /api/events", s.events)
	if s.Web != nil {
		mux.Handle("/", http.FileServerFS(s.Web))
	}
	return s.guard(mux)
}

// guard rejects foreign Host headers and API calls without the token. No CORS headers are
// sent: the app is served from this origin, and other sites must not read the board.
func (s *Server) guard(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.AnyHost {
			host := r.Host
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			if host != "localhost" && host != "127.0.0.1" && host != "::1" && host != "[::1]" {
				httpError(w, http.StatusForbidden, "unexpected Host header")
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && !s.authorized(r) {
			httpError(w, http.StatusUnauthorized, "missing or wrong token: open the link printed by `truehand serve`")
			return
		}
		h.ServeHTTP(w, r)
	})
}

func (s *Server) authorized(r *http.Request) bool {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tok == "" {
		// Images and the event stream cannot always set headers.
		tok = r.URL.Query().Get("token")
	}
	return s.Token != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(s.Token)) == 1
}

// safeJoin joins rel under dir and refuses any symlink along the way, so a link planted in a
// task directory cannot expose files outside it.
func safeJoin(dir, rel string) (string, error) {
	rel = filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("invalid path")
	}
	p := dir
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		p = filepath.Join(p, part)
		fi, err := os.Lstat(p)
		if err != nil {
			return "", err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("symlinks are not served")
		}
	}
	return p, nil
}

type repoInfo struct {
	Name   string         `json:"name"`
	Root   string         `json:"root"`
	Error  string         `json:"error,omitempty"`
	Counts map[string]int `json:"counts"`
}

// repoName is the URL key of a repo: its directory name, de-duplicated by index.
func (s *Server) names() map[string]string {
	out := map[string]string{}
	for i, r := range s.Repos {
		n := filepath.Base(r)
		if _, dup := out[n]; dup {
			n = fmt.Sprintf("%s-%d", n, i)
		}
		out[n] = r
	}
	return out
}

func (s *Server) workspace(w http.ResponseWriter, r *http.Request) *core.Workspace {
	root, ok := s.names()[r.PathValue("repo")]
	if !ok {
		httpError(w, http.StatusNotFound, "unknown repo")
		return nil
	}
	ws, err := core.Open(root)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return nil
	}
	return ws
}

func (s *Server) repos(w http.ResponseWriter, r *http.Request) {
	names := s.names()
	keys := make([]string, 0, len(names))
	for k := range names {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []repoInfo{}
	for _, k := range keys {
		info := repoInfo{Name: k, Root: names[k], Counts: map[string]int{}}
		ws, err := core.Open(names[k])
		if err != nil {
			info.Error = err.Error()
		} else if tasks, err := ws.Store.List(); err == nil {
			for _, t := range tasks {
				info.Counts[string(t.State)]++
			}
		}
		out = append(out, info)
	}
	writeJSON(w, out)
}

type taskSummary struct {
	*board.Task
	Active  bool     `json:"active"`
	Stale   bool     `json:"stale"`
	Missing []string `json:"missing"`
}

func (s *Server) tasks(w http.ResponseWriter, r *http.Request) {
	ws := s.workspace(w, r)
	if ws == nil {
		return
	}
	tasks, err := ws.Store.List()
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	active := ws.ActiveTask()
	out := []taskSummary{}
	for _, t := range tasks {
		ts := taskSummary{Task: t, Active: t.ID == active, Stale: ws.Stale(t), Missing: []string{}}
		if next := t.State.Next(); next != "" {
			if res, err := ws.Check(t, next); err == nil {
				for _, it := range res.Items {
					if it.Status == "fail" {
						ts.Missing = append(ts.Missing, it.Check)
					}
				}
			}
		}
		out = append(out, ts)
	}
	writeJSON(w, out)
}

func (s *Server) task(w http.ResponseWriter, r *http.Request) {
	ws := s.workspace(w, r)
	if ws == nil {
		return
	}
	t, err := ws.Store.Load(r.PathValue("id"))
	if err != nil {
		httpError(w, http.StatusNotFound, err.Error())
		return
	}
	v, err := ws.View(t)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	events, _ := ws.Store.Events(t.ID)
	if events == nil {
		events = []board.Event{}
	}
	writeJSON(w, map[string]any{"view": v, "events": events})
}

func (s *Server) plan(w http.ResponseWriter, r *http.Request) {
	ws := s.workspace(w, r)
	if ws == nil {
		return
	}
	id := r.PathValue("id")
	if err := board.ValidID(id); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	p, err := safeJoin(ws.Store.TaskDir(id), "plan.md")
	if err != nil {
		writeJSON(w, map[string]any{"exists": false, "markdown": "", "problems": []plan.Problem{}})
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		writeJSON(w, map[string]any{"exists": false, "markdown": "", "problems": []plan.Problem{}})
		return
	}
	probs := plan.Check(string(b))
	if probs == nil {
		probs = []plan.Problem{}
	}
	writeJSON(w, map[string]any{"exists": true, "markdown": string(b), "problems": probs, "sha256": evidence.HashBytes(b)})
}

// file serves a file from the task directory (evidence images, context.md).
func (s *Server) file(w http.ResponseWriter, r *http.Request) {
	ws := s.workspace(w, r)
	if ws == nil {
		return
	}
	id := r.PathValue("id")
	if err := board.ValidID(id); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	p, err := safeJoin(ws.Store.TaskDir(id), r.PathValue("path"))
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	http.ServeFile(w, r, p)
}

// approve records a human approval of the plan. The caller must present the token printed
// when the server started, and the sha256 of the plan they read, so an approval never
// covers a plan edited after it was displayed.
func (s *Server) approve(w http.ResponseWriter, r *http.Request) {
	if !s.Approvals {
		httpError(w, http.StatusForbidden, "approvals are disabled: the person who started `truehand serve` did not authenticate")
		return
	}
	ws := s.workspace(w, r)
	if ws == nil {
		return
	}
	t, err := ws.Store.Load(r.PathValue("id"))
	if err != nil {
		httpError(w, http.StatusNotFound, err.Error())
		return
	}
	var body struct {
		PlanSHA string `json:"plan_sha256"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.PlanSHA == "" {
		httpError(w, http.StatusBadRequest, "body must be {\"plan_sha256\": \"...\"}")
		return
	}
	pp, err := safeJoin(ws.Store.TaskDir(t.ID), "plan.md")
	if err != nil {
		httpError(w, http.StatusConflict, "no plan.md")
		return
	}
	b, err := os.ReadFile(pp)
	if err != nil {
		httpError(w, http.StatusConflict, "no plan.md")
		return
	}
	if evidence.HashBytes(b) != body.PlanSHA {
		httpError(w, http.StatusConflict, "plan.md changed since you read it: reload and read it again")
		return
	}
	if probs := plan.Check(string(b)); len(probs) > 0 {
		httpError(w, http.StatusConflict, "plan.md is incomplete")
		return
	}
	a := board.Approval{State: board.Approved, Name: ws.Repo.ConfigValue("user.name"), Email: ws.Repo.ConfigValue("user.email"), PlanSHA: body.PlanSHA, Via: "app+" + humanauth.Method()}
	if err := ws.Store.AppendApproval(t.ID, a); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = ws.Store.AppendEvent(t.ID, board.Event{Type: "approved", Actor: "human:" + a.Email, Detail: map[string]any{"via": "app"}})
	writeJSON(w, map[string]any{"approved": true})
}

// events streams "changed <repo>" whenever a repo's .truehand directory changes.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		httpError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ch := make(chan string, 16)
	s.mu.Lock()
	if s.clients == nil {
		s.clients = map[chan string]bool{}
	}
	s.clients[ch] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, ch)
		s.mu.Unlock()
	}()
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case repo := <-ch:
			fmt.Fprintf(w, "event: changed\ndata: %s\n\n", repo)
			fl.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

func (s *Server) broadcast(repo string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.clients {
		select {
		case ch <- repo:
		default:
		}
	}
}

// Watch polls each repo's .truehand tree (sizes and mtimes) and broadcasts changes.
// Polling keeps the binary free of platform watchers and is cheap at this scale.
func (s *Server) Watch(stop <-chan struct{}, every time.Duration) {
	last := map[string]string{}
	for {
		for name, root := range s.names() {
			sig := signature(filepath.Join(root, ".truehand"))
			if prev, ok := last[name]; ok && prev != sig {
				s.broadcast(name)
			}
			last[name] = sig
		}
		select {
		case <-stop:
			return
		case <-time.After(every):
		}
	}
}

func signature(dir string) string {
	var b strings.Builder
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			fmt.Fprintf(&b, "%s %d %d\n", p, fi.Size(), fi.ModTime().UnixNano())
		}
		return nil
	})
	return evidence.HashBytes([]byte(b.String()))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
