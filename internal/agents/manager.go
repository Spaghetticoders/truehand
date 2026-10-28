package agents

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/spaghetticoders/truehand/internal/appstate"
)

var (
	ErrNotFound  = errors.New("no such session")
	ErrForbidden = errors.New("wrong session secret")
	ErrNotLive   = errors.New("the session is not running")
	ErrLive      = errors.New("the session is still running")
	// ErrNothingToResume: the session never got a prompt, so Claude Code kept no conversation.
	ErrNothingToResume = errors.New("nothing to resume: the session never received a message, start a new one")
)

type Config struct {
	Store     *appstate.Store
	Claude    string // the claude executable
	PluginDir string
	Settings  string
	HookAddr  string // 127.0.0.1:port of the server, for the status hook
	// AllowCwd says whether a directory is a registered project root or task worktree.
	AllowCwd func(dir string) bool
	Env      []string // base environment, cleaned before use; default os.Environ()
	OnChange func(appstate.Session)
	// RingSize is the scrollback kept in memory (default 1 MB); the file on disk is cut back
	// to it when it grows past twice that.
	RingSize  int
	KillGrace time.Duration // between SIGTERM and SIGKILL (default 5 s)
}

type Manager struct {
	cfg   Config
	mu    sync.Mutex
	procs map[string]*Proc
	// resuming holds the sessions being relaunched, so two resumes never start two claude
	// processes on one conversation.
	resuming map[string]bool
}

func NewManager(cfg Config) (*Manager, error) {
	if cfg.Store == nil || cfg.Claude == "" || cfg.AllowCwd == nil {
		return nil, errors.New("agents: store, claude and AllowCwd are required")
	}
	if cfg.Env == nil {
		cfg.Env = os.Environ()
	}
	if cfg.RingSize == 0 {
		cfg.RingSize = 1 << 20
	}
	if cfg.KillGrace == 0 {
		cfg.KillGrace = 5 * time.Second
	}
	return &Manager{cfg: cfg, procs: map[string]*Proc{}}, nil
}

type StartRequest struct {
	Project        string
	Task           string
	Role           string
	Cwd            string
	PermissionMode string
	Prompt         string
	Cols, Rows     uint16
}

// Start launches a new claude session with a fresh session id.
func (m *Manager) Start(req StartRequest) (appstate.Session, error) {
	if !ValidRole(req.Role) {
		return appstate.Session{}, fmt.Errorf("unknown role %q", req.Role)
	}
	cwd, err := m.checkCwd(req.Cwd)
	if err != nil {
		return appstate.Session{}, err
	}
	rec := appstate.Session{
		ID:              appstate.NewID("s_"),
		Project:         req.Project,
		Task:            req.Task,
		Role:            req.Role,
		ClaudeSessionID: NewUUID(),
		Cwd:             cwd,
		PermissionMode:  req.PermissionMode,
	}
	args, err := LaunchArgs(LaunchSpec{Role: rec.Role, PluginDir: m.cfg.PluginDir, Settings: m.cfg.Settings, ClaudeSessionID: rec.ClaudeSessionID, PermissionMode: rec.PermissionMode, Prompt: req.Prompt})
	if err != nil {
		return appstate.Session{}, err
	}
	rec.Args = args
	return m.launch(rec, req.Cols, req.Rows)
}

// Resume relaunches an ended session with claude --resume, in the same directory and role.
func (m *Manager) Resume(sid string, cols, rows uint16) (appstate.Session, error) {
	m.mu.Lock()
	if m.procs[sid] != nil || m.resuming[sid] {
		m.mu.Unlock()
		return appstate.Session{}, ErrLive
	}
	if m.resuming == nil {
		m.resuming = map[string]bool{}
	}
	m.resuming[sid] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.resuming, sid)
		m.mu.Unlock()
	}()
	rec, err := m.record(sid)
	if err != nil {
		return appstate.Session{}, err
	}
	if !rec.Resumable {
		return appstate.Session{}, ErrNothingToResume
	}
	if _, err := m.checkCwd(rec.Cwd); err != nil {
		return appstate.Session{}, err
	}
	// A resumed conversation continues on the plugin it started with while that copy exists.
	pluginDir := m.cfg.PluginDir
	if rec.PluginDir != "" {
		if _, err := os.Stat(filepath.Join(filepath.Dir(rec.PluginDir), ".complete")); err == nil {
			pluginDir = rec.PluginDir
		}
	}
	args, err := LaunchArgs(LaunchSpec{Role: rec.Role, PluginDir: pluginDir, Settings: m.cfg.Settings, ClaudeSessionID: rec.ClaudeSessionID, Resume: true, PermissionMode: rec.PermissionMode})
	if err != nil {
		return appstate.Session{}, err
	}
	rec.Args, rec.PluginDir = args, pluginDir
	rec.Ended, rec.ExitCode, rec.Resumable, rec.Stopped = nil, nil, false, false
	return m.launch(rec, cols, rows)
}

func (m *Manager) checkCwd(dir string) (string, error) {
	if !filepath.IsAbs(dir) {
		return "", errors.New("the session directory must be an absolute path")
	}
	dir = filepath.Clean(dir)
	if !m.cfg.AllowCwd(dir) {
		return "", fmt.Errorf("%s is not a registered project or task worktree", dir)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%s is not a directory", dir)
	}
	return dir, nil
}

func (m *Manager) launch(rec appstate.Session, cols, rows uint16) (appstate.Session, error) {
	if cols == 0 || rows == 0 {
		cols, rows = 120, 40
	}
	secret := appstate.NewSecret()
	cmd := exec.Command(m.cfg.Claude, rec.Args...)
	cmd.Dir = rec.Cwd
	cmd.Env = CleanEnv(m.cfg.Env, SessionEnv(rec.ID, secret, m.cfg.HookAddr))
	logPath, err := m.cfg.Store.ScrollbackPath(rec.ID)
	if err != nil {
		return appstate.Session{}, err
	}
	log, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return appstate.Session{}, err
	}
	logSize := int64(0)
	if fi, err := log.Stat(); err == nil {
		logSize = fi.Size()
	}
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		log.Close()
		return appstate.Session{}, err
	}
	rec.PID = cmd.Process.Pid
	rec.Comm, rec.Lstart = psInfo(rec.PID)
	if rec.PluginDir == "" {
		rec.PluginDir = m.cfg.PluginDir
	}
	rec.Status = appstate.StatusStarting
	rec.Started = time.Now().UTC().Truncate(time.Second)
	rec.Updated = rec.Started
	p := &Proc{
		m: m, rec: rec, secretHash: appstate.HashSecret(secret),
		cmd: cmd, pty: f, log: log, logSize: logSize,
		ring: newRing(m.cfg.RingSize), subs: map[chan []byte]struct{}{},
		cols: cols, rows: rows,
		readDone: make(chan struct{}), done: make(chan struct{}),
	}
	m.mu.Lock()
	m.procs[rec.ID] = p
	m.mu.Unlock()
	// Recorded before wait starts, so an instant exit cannot be overwritten by "starting".
	perr := m.cfg.Store.PutSession(rec)
	go p.read()
	go p.wait()
	if perr != nil {
		p.terminate()
		return appstate.Session{}, perr
	}
	rec = p.Session()
	m.changed(rec)
	return rec, nil
}

func (m *Manager) changed(rec appstate.Session) {
	if m.cfg.OnChange != nil {
		m.cfg.OnChange(rec)
	}
}

func (m *Manager) proc(sid string) *Proc {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.procs[sid]
}

// Proc returns the running process of a session.
func (m *Manager) Proc(sid string) (*Proc, error) {
	if p := m.proc(sid); p != nil {
		return p, nil
	}
	if _, err := m.record(sid); err != nil {
		return nil, err
	}
	return nil, ErrNotLive
}

func (m *Manager) record(sid string) (appstate.Session, error) {
	list, err := m.cfg.Store.Sessions()
	if err != nil {
		return appstate.Session{}, err
	}
	for _, s := range list {
		if s.ID == sid {
			return s, nil
		}
	}
	return appstate.Session{}, ErrNotFound
}

// Get returns a session as last recorded.
func (m *Manager) Get(sid string) (appstate.Session, error) {
	if p := m.proc(sid); p != nil {
		return p.Session(), nil
	}
	return m.record(sid)
}

// Sessions lists every recorded session.
func (m *Manager) Sessions() ([]appstate.Session, error) {
	list, err := m.cfg.Store.Sessions()
	if err != nil {
		return nil, err
	}
	for i := range list {
		if p := m.proc(list[i].ID); p != nil {
			list[i] = p.Session()
		}
	}
	return list, nil
}

// Signal sends "int" (SIGINT, like Ctrl-C) or "term" (SIGTERM, then SIGKILL after the
// grace period) to the session's process group.
func (m *Manager) Signal(sid, sig string) error {
	p, err := m.Proc(sid)
	if err != nil {
		return err
	}
	switch sig {
	case "int":
		return syscall.Kill(-p.rec.PID, syscall.SIGINT)
	case "term":
		p.terminate()
		return nil
	}
	return fmt.Errorf("unknown signal %q (int or term)", sig)
}

// Delete stops the session if it runs and forgets it, scrollback included.
func (m *Manager) Delete(sid string) error {
	if p := m.proc(sid); p != nil {
		p.terminate()
		select {
		case <-p.done:
		case <-time.After(m.cfg.KillGrace + 2*time.Second):
			return errors.New("the session did not stop")
		}
	}
	found := false
	err := m.cfg.Store.UpdateSessions(func(list *[]appstate.Session) error {
		for i := range *list {
			if (*list)[i].ID == sid {
				*list = append((*list)[:i], (*list)[i+1:]...)
				found = true
				return nil
			}
		}
		return ErrNotFound
	})
	if err != nil {
		return err
	}
	if found {
		if p, err := m.cfg.Store.ScrollbackPath(sid); err == nil {
			_ = os.Remove(p)
		}
	}
	return nil
}

// Scrollback is the output kept for a session: live from memory, else from disk.
func (m *Manager) Scrollback(sid string) ([]byte, error) {
	if p := m.proc(sid); p != nil {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.ring.Bytes(), nil
	}
	if _, err := m.record(sid); err != nil {
		return nil, err
	}
	p, err := m.cfg.Store.ScrollbackPath(sid)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if len(b) > m.cfg.RingSize {
		b = b[len(b)-m.cfg.RingSize:]
	}
	return b, err
}

// Tail is the last n lines of a session's terminal, without escape sequences.
func (m *Manager) Tail(sid string, n int) []string {
	var b []byte
	if p := m.proc(sid); p != nil {
		p.mu.Lock()
		b = p.ring.Bytes()
		p.mu.Unlock()
	} else if p, err := m.cfg.Store.ScrollbackPath(sid); err != nil {
		return []string{}
	} else if f, err := os.Open(p); err == nil {
		if fi, err := f.Stat(); err == nil && fi.Size() > tailBytes {
			f.Seek(-tailBytes, 2)
		}
		b, _ = io.ReadAll(io.LimitReader(f, tailBytes))
		f.Close()
	}
	if len(b) > tailBytes {
		b = b[len(b)-tailBytes:]
	}
	return TailLines(b, n)
}

const tailBytes = 16 << 10

var ansiRe = regexp.MustCompile(`\x1b(\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(\x07|\x1b\\)|[@-Z\\-_])`)

// TailLines strips terminal escapes from b and returns its last n non-blank lines. A carriage
// return starts the line over, as a terminal would draw it.
func TailLines(b []byte, n int) []string {
	text := ansiRe.ReplaceAllString(string(b), "")
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		if i := strings.LastIndex(strings.TrimRight(l, "\r"), "\r"); i >= 0 {
			l = l[i+1:]
		}
		l = strings.TrimRight(strings.Map(func(r rune) rune {
			if r < 0x20 && r != '\t' {
				return -1
			}
			return r
		}, l), " \t")
		if l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	if lines == nil {
		lines = []string{}
	}
	return lines
}

type hookInput struct {
	Event            string `json:"hook_event_name"`
	NotificationType string `json:"notification_type"`
	Message          string `json:"message"`
}

// Hook applies a status hook report. The secret is the one handed to that session only.
func (m *Manager) Hook(sid, secret string, payload []byte) error {
	p := m.proc(sid)
	if p == nil {
		return ErrNotFound
	}
	if !appstate.MatchSecret(p.secretHash, secret) {
		return ErrForbidden
	}
	var in hookInput
	if err := json.Unmarshal(payload, &in); err != nil {
		return fmt.Errorf("hook payload: %w", err)
	}
	if in.Event == "UserPromptSubmit" {
		p.setPrompted()
	}
	p.setStatus(nextStatus(p.Session().Status, in))
	return nil
}

func nextStatus(cur string, in hookInput) string {
	if !appstate.Live(cur) {
		return cur
	}
	switch in.Event {
	case "SessionStart":
		// Hooks run async, so this may arrive after the first prompt was submitted.
		if cur == appstate.StatusStarting {
			return appstate.StatusIdle
		}
	case "UserPromptSubmit", "PostToolUse":
		return appstate.StatusWorking
	case "Stop":
		return appstate.StatusIdle
	case "Notification":
		switch {
		case in.NotificationType == "permission_prompt",
			in.NotificationType == "" && strings.Contains(strings.ToLower(in.Message), "permission"):
			return appstate.StatusWaitingPermission
		case in.NotificationType == "idle_prompt", in.NotificationType == "elicitation_dialog", in.NotificationType == "":
			return appstate.StatusWaitingInput
		}
	}
	return cur
}

// CleanupOrphans settles sessions recorded as running by a server that is gone: a process
// still alive with the same command and start time is terminated with its group; either way
// the session is marked ended and resumable.
func (m *Manager) CleanupOrphans() error {
	var changed []appstate.Session
	err := m.cfg.Store.UpdateSessions(func(list *[]appstate.Session) error {
		for i := range *list {
			s := &(*list)[i]
			if !appstate.Live(s.Status) || m.proc(s.ID) != nil {
				continue
			}
			if s.PID > 0 && s.Lstart != "" {
				if comm, lstart := psInfo(s.PID); comm == s.Comm && lstart == s.Lstart {
					_ = syscall.Kill(-s.PID, syscall.SIGTERM)
				}
			}
			t := time.Now().UTC().Truncate(time.Second)
			s.Status, s.Ended, s.Updated, s.Resumable = appstate.StatusExited, &t, t, s.ClaudeSessionID != "" && s.Prompted
			changed = append(changed, *s)
		}
		return nil
	})
	for _, s := range changed {
		m.changed(s)
	}
	return err
}

// psInfo returns the command name and start time of pid, which together tell a process from
// a later one that reused its pid.
func psInfo(pid int) (comm, lstart string) {
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", ""
	}
	comm = strings.TrimSpace(string(out))
	out, err = exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", ""
	}
	return comm, strings.Join(strings.Fields(string(out)), " ")
}
