// Package serve is the local HTTP API behind the Truehand app: projects and their boards,
// tasks, agent sessions in terminals, and the human-only actions (approve, push, PR), which
// need the Mac app's in-memory secret and an OS authentication every time.
package serve

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spaghetticoders/truehand/internal/agents"
	"github.com/spaghetticoders/truehand/internal/appstate"
	"github.com/spaghetticoders/truehand/internal/humanauth"
)

// Runner runs a program in dir; tests replace it for git push and gh.
type Runner func(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error)

type Server struct {
	Store  *appstate.Store
	Agents *agents.Manager // nil when sessions are unavailable (read-only, or no claude)
	// ReadOnly refuses every change: set when serve runs inside an agent session.
	ReadOnly bool
	// Hosts are accepted Host names besides loopback (tailnet IP, MagicDNS name).
	Hosts   []string
	Web     fs.FS
	Version string
	// ShellSecretHash is the sha256 of the secret the Mac app holds in memory. Only requests
	// that carry it may ask for an OS authentication.
	ShellSecretHash string
	// HumanAuth asks the OS to authenticate the user; tests replace it.
	HumanAuth func(reason string) error
	// TruehandBin is the directory put first on PATH for git push, so the pre-push hook finds
	// this binary.
	TruehandBin  string
	WorktreesDir string // ~/.truehand/worktrees
	Home         string // the file browser stays under it
	Run          Runner
	LookPath     func(string) (string, error)
	// Shutdown stops the server gracefully, as SIGTERM does; set by the serve command.
	Shutdown func()
	// ApplyRemote opens or closes remote access at once after the settings change; set by
	// the serve command, which owns the listeners. It returns the Host names to accept.
	ApplyRemote func(appstate.Remote) ([]string, error)
	// RemoteStatus is remote access as it really is: "off", "listening" or "error" with why.
	RemoteStatus func() (state, msg string)
	// TailnetIP finds this Mac's Tailscale address when the settings name none.
	TailnetIP func() string
	// MacKey is the public key the Mac app signs intents with (its Secure Enclave key), as
	// it registered it; nil until then.
	MacKey *ecdsa.PublicKey
	// Remotes are the machines reached over ssh; nil on a peer.
	Remotes *Remotes
	// Peer is set on `serve --peer`: a remote machine's server, reached by the Mac through
	// its unix socket only.
	Peer bool

	mu       sync.Mutex
	clients  map[chan sseEvent]bool
	pairs    map[string]time.Time // sha256 of pending pairing codes → expiry
	pairFail []time.Time
	prs      map[string]prState // branch → last known PR, for the waiting list
	health   *healthCache

	waitingDirty atomic.Bool
	// sessionsMu makes "is the checkout free?" and starting a session one step.
	sessionsMu sync.Mutex

	closeOnce sync.Once
	closing   chan struct{}
	boards    map[string]cachedBoard
	probing   bool
	probed    chan struct{} // closed when the first tool probe is done
	// waitedProbe: one request already waited for the first probe; the next ones do not.
	waitedProbe bool
}

func (s *Server) closingCh() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing == nil {
		s.closing = make(chan struct{})
	}
	return s.closing
}

// Close ends the event streams, which never finish on their own; pass it to
// http.Server.RegisterOnShutdown so a shutdown does not wait for them.
func (s *Server) Close() { s.closeOnce.Do(func() { close(s.closingCh()) }) }

func (s *Server) defaults() {
	if s.HumanAuth == nil {
		s.HumanAuth = humanauth.Authenticate
	}
	if s.Run == nil {
		s.Run = runCmd
	}
	if s.LookPath == nil {
		s.LookPath = exec.LookPath
	}
	if s.Home == "" {
		s.Home, _ = os.UserHomeDir()
	}
}

func runCmd(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	return cmd.CombinedOutput()
}

func (s *Server) Handler() http.Handler {
	h := s.guard(s.routeRemote(s.routes()))
	go s.tools(context.Background())
	return h
}

func (s *Server) routes() *http.ServeMux {
	s.defaults()
	mux := http.NewServeMux()
	h := func(pattern string, fn http.HandlerFunc) { mux.HandleFunc(pattern, fn) }

	h("POST /api/login", s.login)
	h("POST /api/pair", s.pair)
	h("POST /api/pair/code", s.pairCode)
	h("GET /api/me", s.me)
	h("GET /api/devices", s.devices)
	h("DELETE /api/devices/{id}", s.deleteDevice)
	h("POST /api/token/rotate", s.rotateToken)
	h("POST /api/shutdown", s.shutdown)
	h("POST /api/shell/challenge", s.shellChallenge)
	h("POST /api/shell/key", s.shellKey)
	h("GET /api/version", s.version)
	h("GET /api/settings", s.getSettings)
	h("PUT /api/settings", s.putSettings)

	h("GET /api/snapshot", s.snapshot)
	h("POST /api/projects", s.addProject)
	h("PATCH /api/projects/{pid}", s.patchProject)
	h("DELETE /api/projects/{pid}", s.deleteProject)
	h("GET /api/fs/dirs", s.fsDirs)
	h("GET /api/projects/{pid}/activation", s.activation)
	h("POST /api/projects/{pid}/activate", s.activate)
	h("PUT /api/projects/{pid}/config", s.putConfig)
	h("POST /api/projects/{pid}/fetch", s.fetch)
	h("POST /api/projects/{pid}/commit-config", s.commitConfig)
	h("POST /api/projects/{pid}/sessions", s.startFreeSession)

	h("POST /api/projects/{pid}/tasks", s.newTask)
	h("GET /api/projects/{pid}/tasks/{id}", s.task)
	h("PATCH /api/projects/{pid}/tasks/{id}", s.editTask)
	h("POST /api/projects/{pid}/tasks/{id}/comments", s.comment)
	h("POST /api/projects/{pid}/tasks/{id}/actions/{action}", s.act)
	h("GET /api/projects/{pid}/tasks/{id}/files/{path...}", s.file)
	h("GET /api/projects/{pid}/tasks/{id}/diff", s.diff)
	h("GET /api/projects/{pid}/tasks/{id}/pr", s.getPR)
	h("POST /api/projects/{pid}/tasks/{id}/intent", s.intent)
	h("DELETE /api/projects/{pid}/tasks/{id}/worktree", s.deleteWorktree)

	h("GET /api/queued", s.queued)
	h("POST /api/queued/{qid}/confirm", s.confirmQueued)
	h("DELETE /api/queued/{qid}", s.discardQueued)

	h("POST /api/sessions/{sid}/resume", s.resumeSession)
	h("POST /api/sessions/{sid}/signal", s.signalSession)
	h("DELETE /api/sessions/{sid}", s.deleteSession)
	h("GET /api/sessions/{sid}/scrollback", s.scrollback)
	h("GET /api/sessions/{sid}/pty", s.pty)

	h("GET /api/machines", s.listMachines)
	h("POST /api/machines", s.addMachine)
	h("POST /api/machines/{mid}/reconnect", s.reconnectMachine)
	h("POST /api/machines/{mid}/update", s.updateMachine)
	h("PATCH /api/machines/{mid}", s.patchMachine)
	h("DELETE /api/machines/{mid}", s.deleteMachine)
	h("GET /api/machines/{mid}/fs/dirs", s.machineDirs)

	h("GET /api/events", s.events)
	h("POST /hook/status", s.statusHook)

	h("/api/", func(w http.ResponseWriter, r *http.Request) { httpError(w, http.StatusNotFound, "no such endpoint") })
	mux.Handle("/", s.static())
	return mux
}

func isUnsafe(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

// guard applies the rules every request obeys before reaching a handler: a known Host
// (against DNS rebinding), Origin equal to Host on anything that changes state and on
// WebSockets (against cross-site requests), no changes at all in read-only mode, and a
// credential on the API.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.allowedHost(r.Host) {
			httpError(w, http.StatusForbidden, "unexpected Host header")
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		hook := r.URL.Path == "/hook/status"
		ws := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
		if (isUnsafe(r.Method) || ws) && !hook && !sameOrigin(r) {
			httpError(w, http.StatusForbidden, "cross-origin request refused")
			return
		}
		if s.ReadOnly && isUnsafe(r.Method) && r.URL.Path != "/api/login" {
			httpError(w, http.StatusForbidden, "this server is read-only: it runs inside an agent session")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && !(r.Method == http.MethodPost && (r.URL.Path == "/api/pair" || r.URL.Path == "/api/shell/challenge")) {
			id, ok := s.authenticate(r)
			if !ok {
				httpError(w, http.StatusUnauthorized, "not signed in: open the Truehand app, or pair this device")
				return
			}
			// Requests from a browser must come with its Origin when authenticated by cookie;
			// only non-browser clients (the Mac app's backend, scripts) use the bearer token.
			if id.Via == viaDevice && (isUnsafe(r.Method) || ws) && r.Header.Get("Origin") == "" {
				httpError(w, http.StatusForbidden, "missing Origin")
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), identityKey{}, id))
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) allowedHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	s.mu.Lock()
	hosts := s.Hosts
	s.mu.Unlock()
	for _, h := range hosts {
		if strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}

// SetHosts replaces the accepted remote Host names.
func (s *Server) SetHosts(hosts []string) {
	s.mu.Lock()
	s.Hosts = hosts
	s.mu.Unlock()
}

// sameOrigin requires the Origin, when sent, to be this very Host. Its absence is checked
// later, per credential.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	return o == "http://"+r.Host || o == "https://"+r.Host
}

func (s *Server) static() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			httpError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		host := r.Host
		w.Header().Set("Content-Security-Policy", fmt.Sprintf("default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self' ws://%s wss://%s; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'", host, host))
		if s.Web == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, placeholderPage)
			return
		}
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if fi, err := fs.Stat(s.Web, p); err != nil || fi.IsDir() {
			// Client-side routes all load the app.
			p = "index.html"
		}
		http.ServeFileFS(w, r, s.Web, p)
	})
}

const placeholderPage = `<!doctype html><meta charset="utf-8"><title>Truehand</title>
<p>The Truehand web app is not built into this binary. Build it with <code>make app</code> (see README).</p>`

// decode reads a JSON body into v, refusing unknown fields: no endpoint takes free-form
// parameters it would silently ignore.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		httpError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) { writeJSONStatus(w, http.StatusOK, v) }

func writeJSONStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

var errStatus = map[error]int{
	appstate.ErrNotFound:      http.StatusNotFound,
	appstate.ErrExists:        http.StatusConflict,
	agents.ErrNotFound:        http.StatusNotFound,
	agents.ErrNotLive:         http.StatusConflict,
	agents.ErrLive:            http.StatusConflict,
	agents.ErrNothingToResume: http.StatusConflict,
	agents.ErrClosed:          http.StatusConflict,
}

func fail(w http.ResponseWriter, err error) {
	var br badRequest
	var bz busy
	switch {
	case errors.As(err, &br):
		httpError(w, http.StatusBadRequest, err.Error())
		return
	case errors.As(err, &bz):
		httpError(w, http.StatusConflict, err.Error())
		return
	}
	for e, code := range errStatus {
		if errors.Is(err, e) {
			httpError(w, code, err.Error())
			return
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		httpError(w, http.StatusNotFound, err.Error())
		return
	}
	httpError(w, http.StatusInternalServerError, err.Error())
}
