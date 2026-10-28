package serve

import (
	"context"
	"net"
	"net/http"
	"runtime"
	"strings"
)

// socketDenied are the endpoints the unix socket never serves: credentials, pairing,
// settings and machines belong to the Mac app.
var socketDenied = []string{"/api/login", "/api/pair", "/api/token/rotate", "/api/devices", "/api/settings", "/api/machines"}

// SocketHandler serves the API on the unix socket (0600, this user only): to `truehand
// sessions` and `truehand attach`, and on a peer to the Mac through ssh. No browser can
// reach it, so there is no Host, Origin or token check; no action that needs a person at
// the Mac is available on it.
//
// Every process of this user can connect, agents included. A connection is trusted only when
// the process behind it is known and is not part of an agent session; any other may read,
// and watch terminals without typing, but changes nothing.
func (s *Server) SocketHandler() http.Handler {
	mux := s.routeRemote(s.routes())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trusted, _ := r.Context().Value(socketTrustKey{}).(bool)
		// The marker can only take rights away: a forwarding Mac sets it for its watchers.
		viewOnly := !trusted || r.Header.Get(viewOnlyHeader) != ""
		for _, p := range socketDenied {
			if r.URL.Path == p || strings.HasPrefix(r.URL.Path, p+"/") {
				httpError(w, http.StatusForbidden, "not available on the terminal socket")
				return
			}
		}
		if s.ReadOnly && isUnsafe(r.Method) {
			httpError(w, http.StatusForbidden, "this server is read-only: it runs inside an agent session")
			return
		}
		if viewOnly && isUnsafe(r.Method) && r.URL.Path != "/hook/status" {
			httpError(w, http.StatusForbidden, "the socket is read-only from an agent session")
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/hook/status" {
			httpError(w, http.StatusNotFound, "only the API is served on the socket")
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), identityKey{}, identity{Via: viaSocket, ViewOnly: viewOnly}))
		mux.ServeHTTP(w, r)
	})
}

type socketTrustKey struct{}

// viewOnlyHeader marks a request forwarded for a caller that may watch but not type.
const viewOnlyHeader = "X-Truehand-View-Only"

// AgentProcess reports whether a process belongs to an agent session (it or an ancestor is
// Claude Code). Unset, every socket connection counts as an agent's.
var AgentProcess func(pid int) bool

// SocketConnContext is the socket server's ConnContext: it decides once per connection
// whether the process behind it may change anything.
func (s *Server) SocketConnContext(ctx context.Context, c net.Conn) context.Context {
	pid := peerPID(c)
	trusted := pid > 0 && AgentProcess != nil && !AgentProcess(pid)
	return context.WithValue(ctx, socketTrustKey{}, trusted)
}

func (s *Server) version(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"version": s.Version, "os": runtime.GOOS, "arch": runtime.GOARCH})
}
