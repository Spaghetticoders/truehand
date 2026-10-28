package serve

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/spaghetticoders/truehand/internal/agents"
)

// statusHook receives `truehand hook status` from the sessions this server started. It
// only ever changes the cosmetic status of the session whose secret it carries.
func (s *Server) statusHook(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(host); identityOf(r).Via != viaSocket && (ip == nil || !ip.IsLoopback()) {
		httpError(w, http.StatusForbidden, "status hooks come from this machine only")
		return
	}
	if s.Agents == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	sid, secret, _ := strings.Cut(r.Header.Get("X-Truehand-Session"), ":")
	body, err := io.ReadAll(io.LimitReader(r.Body, 256<<10))
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch err := s.Agents.Hook(sid, secret, body); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, agents.ErrForbidden):
		httpError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, agents.ErrNotFound):
		httpError(w, http.StatusNotFound, err.Error())
	default:
		httpError(w, http.StatusBadRequest, err.Error())
	}
}
