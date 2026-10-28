package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"time"

	"github.com/coder/websocket"

	"github.com/spaghetticoders/truehand/internal/agents"
	"github.com/spaghetticoders/truehand/internal/appstate"
)

func (s *Server) needAgents(w http.ResponseWriter) bool {
	if s.Agents == nil {
		httpError(w, http.StatusServiceUnavailable, "agent sessions are unavailable: Claude Code (claude) was not found when the server started")
		return false
	}
	return true
}

func (s *Server) permissionMode(asked string) (string, error) {
	if asked == "" {
		st, err := s.Store.State()
		if err != nil {
			return "", err
		}
		asked = st.Settings.DefaultPermissionMode
	}
	if !appstate.ValidPermissionMode(asked) {
		return "", errBadRequest("permission_mode must be one of default, acceptEdits, plan")
	}
	return asked, nil
}

// checkoutBusy enforces one live session per checkout outside task worktrees; in a task's
// worktree its dev and review sessions may run side by side, one per role.
func (s *Server) checkoutBusy(cwd, task, role, exclude string, worktree bool) error {
	list, err := s.Agents.Sessions()
	if err != nil {
		return err
	}
	for _, x := range list {
		if x.ID == exclude || !appstate.Live(x.Status) {
			continue
		}
		if x.Cwd == cwd && (!worktree || x.Task != task) {
			return errBusy("a session is already running in " + cwd)
		}
		if task != "" && x.Task == task && x.Role == role {
			return errBusy("a " + role + " session of " + task + " is already running")
		}
	}
	return nil
}

type busy string

func (e busy) Error() string { return string(e) }

func errBusy(msg string) error { return busy(msg) }

func (s *Server) startFreeSession(w http.ResponseWriter, r *http.Request) {
	if !s.needAgents(w) {
		return
	}
	p, ok := s.projectByID(w, r)
	if !ok {
		return
	}
	var body struct {
		PermissionMode string `json:"permissionMode"`
		Prompt         string `json:"prompt"`
		Cols           uint16 `json:"cols"`
		Rows           uint16 `json:"rows"`
	}
	if r.ContentLength != 0 && !decode(w, r, &body) {
		return
	}
	mode, err := s.permissionMode(body.PermissionMode)
	if err != nil {
		fail(w, err)
		return
	}
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	if err := s.checkoutBusy(p.Root, "", agents.RoleFree, "", false); err != nil {
		fail(w, err)
		return
	}
	rec, err := s.Agents.Start(agents.StartRequest{Project: p.ID, Role: agents.RoleFree, Cwd: p.Root, PermissionMode: mode, Prompt: body.Prompt, Cols: body.Cols, Rows: body.Rows})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, viewSession(rec, nil))
}

func (s *Server) resumeSession(w http.ResponseWriter, r *http.Request) {
	if !s.needAgents(w) {
		return
	}
	var body struct {
		Cols uint16 `json:"cols"`
		Rows uint16 `json:"rows"`
	}
	if r.ContentLength != 0 && !decode(w, r, &body) {
		return
	}
	rec, err := s.Agents.Get(r.PathValue("sid"))
	if err != nil {
		fail(w, err)
		return
	}
	out, err := s.resume(rec, body.Cols, body.Rows)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, viewSession(out, nil))
}

func (s *Server) inWorktree(rec appstate.Session) bool {
	st, err := s.Store.State()
	if err != nil {
		return false
	}
	p, ok := st.Project(rec.Project)
	if !ok {
		return false
	}
	a, err1 := filepath.EvalSymlinks(rec.Cwd)
	b, err2 := filepath.EvalSymlinks(p.Root)
	return err1 == nil && err2 == nil && a != b
}

func (s *Server) reviewIsIndependent(rec appstate.Session) error {
	if rec.Role != agents.RoleReview {
		return nil
	}
	st, err := s.Store.State()
	if err != nil {
		return err
	}
	p, ok := st.Project(rec.Project)
	if !ok {
		return appstate.ErrNotFound
	}
	l, err := s.locateIn(*p, rec.Task)
	if err != nil {
		return err
	}
	workers, err := l.WS.Workers(l.Task)
	if err != nil {
		return err
	}
	if workers["claude:"+rec.ClaudeSessionID] {
		return errBusy("this session worked on " + rec.Task + ": it cannot review it")
	}
	return nil
}

func (s *Server) signalSession(w http.ResponseWriter, r *http.Request) {
	if !s.needAgents(w) {
		return
	}
	var body struct {
		Sig string `json:"sig"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Sig != "int" && body.Sig != "term" {
		httpError(w, http.StatusBadRequest, "sig must be int or term")
		return
	}
	if err := s.Agents.Signal(r.PathValue("sid"), body.Sig); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	if !s.needAgents(w) {
		return
	}
	if err := s.Agents.Delete(r.PathValue("sid")); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) scrollback(w http.ResponseWriter, r *http.Request) {
	if !s.needAgents(w) {
		return
	}
	b, err := s.Agents.Scrollback(r.PathValue("sid"))
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
}

// taskProc is a live session of the located task.
func (s *Server) taskProc(l *located, sid string) (*agents.Proc, error) {
	if s.Agents == nil {
		return nil, agents.ErrNotLive
	}
	rec, err := s.Agents.Get(sid)
	if err != nil {
		return nil, err
	}
	if rec.Project != l.Project.ID || rec.Task != l.Task.ID {
		return nil, errBadRequest("that session belongs to another task")
	}
	return s.Agents.Proc(sid)
}

type ptyMsg struct {
	T string `json:"t"`
	D string `json:"d,omitempty"`
	C uint16 `json:"c,omitempty"`
	R uint16 `json:"r,omitempty"`
}

func wsWrite(ctx context.Context, c *websocket.Conn, typ websocket.MessageType, b []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.Write(ctx, typ, b)
}

func wsJSON(ctx context.Context, c *websocket.Conn, v any) error {
	b, _ := json.Marshal(v)
	return wsWrite(ctx, c, websocket.MessageText, b)
}

func wsChunks(ctx context.Context, c *websocket.Conn, b []byte) error {
	for len(b) > 0 {
		n := min(len(b), 64<<10)
		if err := wsWrite(ctx, c, websocket.MessageBinary, b[:n]); err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

func helloMsg(rec appstate.Session, cols, rows uint16) map[string]any {
	v := viewSession(rec, nil)
	m := map[string]any{"t": "hello", "cols": cols, "rows": rows, "status": v.Status}
	if v.WaitingFor != "" {
		m["waitingFor"] = v.WaitingFor
	}
	return m
}

func exitMsg(rec appstate.Session) map[string]any {
	m := map[string]any{"t": "exit", "status": viewSession(rec, nil).Status}
	if rec.ExitCode != nil {
		m["code"] = *rec.ExitCode
	}
	return m
}

// pty streams a session's terminal: a hello, the scrollback, then live output as binary
// messages; the client sends {"t":"in","d":…} and {"t":"resize","c":…,"r":…} as text.
func (s *Server) pty(w http.ResponseWriter, r *http.Request) {
	if !s.needAgents(w) {
		return
	}
	sid := r.PathValue("sid")
	rec, err := s.Agents.Get(sid)
	if err != nil {
		fail(w, err)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(64 << 10)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	proc, err := s.Agents.Proc(sid)
	if err != nil {
		b, _ := s.Agents.Scrollback(sid)
		_ = wsJSON(ctx, c, helloMsg(rec, 0, 0))
		_ = wsChunks(ctx, c, b)
		_ = wsJSON(ctx, c, exitMsg(rec))
		c.Close(websocket.StatusNormalClosure, "")
		return
	}
	hist, ch, unsubscribe := proc.Subscribe()
	defer unsubscribe()
	cols, rows := proc.Size()
	if wsJSON(ctx, c, helloMsg(proc.Session(), cols, rows)) != nil || wsChunks(ctx, c, hist) != nil {
		return
	}
	go func() {
		defer cancel()
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			if typ != websocket.MessageText || s.ReadOnly || identityOf(r).ViewOnly {
				continue
			}
			var m ptyMsg
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			switch m.T {
			case "in":
				if proc.Write([]byte(m.D)) != nil {
					return
				}
			case "resize":
				_ = proc.Resize(m.C, m.R)
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case b, ok := <-ch:
			if !ok {
				select {
				case <-proc.Done():
					rec, _ := s.Agents.Get(sid)
					_ = wsJSON(ctx, c, exitMsg(rec))
					c.Close(websocket.StatusNormalClosure, "")
				case <-time.After(3 * time.Second):
					c.Close(websocket.StatusTryAgainLater, "viewer fell behind: reconnect")
				}
				return
			}
			if err := wsWrite(ctx, c, websocket.MessageBinary, b); err != nil {
				return
			}
		}
	}
}
