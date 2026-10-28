package serve

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/spaghetticoders/truehand/internal/appstate"
	"github.com/spaghetticoders/truehand/internal/machines"
)

func (s *Server) needRemotes(w http.ResponseWriter) bool {
	if s.Remotes == nil {
		httpError(w, http.StatusServiceUnavailable, "remote machines are unavailable: no ssh on this Mac")
		return false
	}
	return true
}

func (s *Server) listMachines(w http.ResponseWriter, r *http.Request) {
	st, err := s.Store.State()
	if err != nil {
		fail(w, err)
		return
	}
	out := []machineView{s.localMachine(r.Context())}
	for _, m := range st.Machines {
		out = append(out, s.machineView(m))
	}
	writeJSON(w, out)
}

// provision installs this truehand on the machine when its copy differs, restarting its peer
// on the new binary, and reports the machine's status.
func (s *Server) provision(ctx context.Context, m appstate.Machine, force bool) (machines.Info, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	info, _, err := machines.Provision(ctx, s.Remotes.get(m).peer, s.Version, s.Remotes.BinaryFor, force)
	if err != nil {
		return info, err
	}
	r := s.Remotes.get(m)
	r.mu.Lock()
	r.info = info
	r.mu.Unlock()
	if err := s.Store.UpdateState(func(st *appstate.State) error {
		if x, ok := st.Machine(m.ID); ok {
			x.OS, x.Arch = info.OS, info.Arch
		}
		return nil
	}); err != nil {
		return info, err
	}
	s.check(ctx, m)
	return info, nil
}

// addMachine checks the machine over ssh, installs truehand in its ~/.truehand and starts
// its peer. Reaching another computer as this user needs the person at the Mac.
func (s *Server) addMachine(w http.ResponseWriter, r *http.Request) {
	if !s.needRemotes(w) {
		return
	}
	var body struct {
		SSHTarget string `json:"sshTarget"`
		Label     string `json:"label"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !appstate.ValidSSHTarget(body.SSHTarget) {
		httpError(w, http.StatusBadRequest, "sshTarget must be a host as in ~/.ssh/config (name, alias or user@host)")
		return
	}
	if !s.human(w, r, "Truehand: connect "+body.SSHTarget+" and install Truehand in its ~/.truehand", nil) {
		return
	}
	m, err := s.Store.AddMachine(appstate.Machine{SSHTarget: body.SSHTarget, Label: body.Label})
	if err != nil {
		fail(w, err)
		return
	}
	if _, err := s.provision(r.Context(), m, false); err != nil {
		_ = s.Store.RemoveMachine(m.ID)
		s.Remotes.forget(m.ID)
		writeJSONStatus(w, http.StatusBadGateway, map[string]string{"error": err.Error(), "code": "machine-unreachable"})
		return
	}
	m, _ = s.machineByID(m.ID)
	writeJSONStatus(w, http.StatusCreated, s.machineView(m))
}

func (s *Server) machineOf(w http.ResponseWriter, r *http.Request) (appstate.Machine, bool) {
	if !s.needRemotes(w) {
		return appstate.Machine{}, false
	}
	m, ok := s.machineByID(r.PathValue("mid"))
	if !ok {
		httpError(w, http.StatusNotFound, "unknown machine")
	}
	return m, ok
}

func (s *Server) reconnectMachine(w http.ResponseWriter, r *http.Request) {
	m, ok := s.machineOf(w, r)
	if !ok {
		return
	}
	s.Remotes.get(m).peer.HTTP.CloseIdleConnections()
	s.check(r.Context(), m)
	writeJSON(w, s.machineView(m))
}

func (s *Server) updateMachine(w http.ResponseWriter, r *http.Request) {
	m, ok := s.machineOf(w, r)
	if !ok {
		return
	}
	if !s.human(w, r, "Truehand: update Truehand on "+m.Label, nil) {
		return
	}
	if _, err := s.provision(r.Context(), m, true); err != nil {
		writeJSONStatus(w, http.StatusBadGateway, map[string]string{"error": err.Error(), "code": "machine-unreachable"})
		return
	}
	writeJSON(w, s.machineView(m))
}

func (s *Server) patchMachine(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Label   *string `json:"label"`
		Enabled *bool   `json:"enabled"`
	}
	if !decode(w, r, &body) {
		return
	}
	var out appstate.Machine
	err := s.Store.UpdateState(func(st *appstate.State) error {
		m, ok := st.Machine(r.PathValue("mid"))
		if !ok {
			return appstate.ErrNotFound
		}
		if body.Label != nil {
			if strings.TrimSpace(*body.Label) == "" {
				return errBadRequest("label must not be empty")
			}
			m.Label = strings.TrimSpace(*body.Label)
		}
		if body.Enabled != nil {
			m.Disabled = !*body.Enabled
		}
		out = *m
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	s.broadcast(sseEvent{"machine", map[string]string{"id": out.ID, "status": s.machineView(out).Status}})
	writeJSON(w, s.machineView(out))
}

// deleteMachine forgets the machine here; its own projects and sessions stay there.
func (s *Server) deleteMachine(w http.ResponseWriter, r *http.Request) {
	mid := r.PathValue("mid")
	if err := s.Store.RemoveMachine(mid); err != nil {
		fail(w, err)
		return
	}
	if s.Remotes != nil {
		s.Remotes.forget(mid)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) machineDirs(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.machineOf(w, r); !ok {
		return
	}
	s.forward(w, r, r.PathValue("mid"), "/api/fs/dirs")
}
