package appstate

import "time"

// PermissionModes are the Claude Code permission modes a session may start in. Modes that
// skip permission prompts are deliberately absent.
var PermissionModes = []string{"default", "acceptEdits", "plan"}

func ValidPermissionMode(m string) bool { return contains(PermissionModes, m) }

const (
	StatusStarting          = "starting"
	StatusWorking           = "working"
	StatusWaitingInput      = "waiting_input"
	StatusWaitingPermission = "waiting_permission"
	StatusIdle              = "idle"
	StatusExited            = "exited"
	StatusCrashed           = "crashed"
)

// Live reports whether a status belongs to a running process.
func Live(status string) bool { return status != StatusExited && status != StatusCrashed }

type Session struct {
	ID              string     `json:"id"`
	Project         string     `json:"project"`
	Task            string     `json:"task,omitempty"`
	Role            string     `json:"role"` // dev | review | free
	ClaudeSessionID string     `json:"claude_session_id"`
	Status          string     `json:"status"`
	PID             int        `json:"pid,omitempty"`
	Comm            string     `json:"comm,omitempty"`
	Lstart          string     `json:"lstart,omitempty"`
	Cwd             string     `json:"cwd"`
	Args            []string   `json:"args"`
	PluginDir       string     `json:"plugin_dir"`
	PermissionMode  string     `json:"permission_mode"`
	Started         time.Time  `json:"started"`
	Updated         time.Time  `json:"updated"` // when Status last changed
	Ended           *time.Time `json:"ended,omitempty"`
	ExitCode        *int       `json:"exit_code,omitempty"`
	Resumable       bool       `json:"resumable,omitempty"`
	// Stopped is set when the app ended the session on request, so its exit status (143
	// after SIGTERM) is not a failure.
	Stopped bool `json:"stopped,omitempty"`
	// Prompted is set once a prompt was submitted: before that Claude Code keeps no
	// conversation, and --resume finds nothing.
	Prompted bool `json:"prompted,omitempty"`
}

type sessionsFileT struct {
	Sessions []Session `json:"sessions"`
}

func initSessions(f *sessionsFileT) {
	if f.Sessions == nil {
		f.Sessions = []Session{}
	}
}

func (s *Store) Sessions() ([]Session, error) {
	f, err := load(s, sessionsFile, initSessions)
	if err != nil {
		return nil, err
	}
	return f.Sessions, nil
}

func (s *Store) UpdateSessions(fn func(*[]Session) error) error {
	return update(s, sessionsFile, initSessions, func(f *sessionsFileT) error { return fn(&f.Sessions) })
}

// PutSession inserts or replaces the session with the same id.
func (s *Store) PutSession(v Session) error {
	return s.UpdateSessions(func(list *[]Session) error {
		for i := range *list {
			if (*list)[i].ID == v.ID {
				(*list)[i] = v
				return nil
			}
		}
		*list = append(*list, v)
		return nil
	})
}

// PluginDirsInUse are the plugin directories of sessions that are alive or can be resumed.
func (s *Store) PluginDirsInUse() (map[string]bool, error) {
	list, err := s.Sessions()
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, v := range list {
		if v.PluginDir != "" && (Live(v.Status) || v.Resumable) {
			out[v.PluginDir] = true
		}
	}
	return out, nil
}
