package appstate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type State struct {
	Version  int       `json:"version"`
	Projects []Project `json:"projects"`
	Machines []Machine `json:"machines"`
	Settings Settings  `json:"settings"`
}

// LocalMachine is the id of the machine the server runs on.
const LocalMachine = "local"

// Machine is another computer reached over ssh, whose own `truehand serve --peer` holds its
// projects and sessions.
type Machine struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	SSHTarget string    `json:"ssh_target"`
	OS        string    `json:"os,omitempty"`
	Arch      string    `json:"arch,omitempty"`
	Disabled  bool      `json:"disabled,omitempty"`
	Added     time.Time `json:"added"`
}

type Project struct {
	ID     string    `json:"id"`
	Root   string    `json:"root"`
	Client string    `json:"client"`
	Name   string    `json:"name"`
	Added  time.Time `json:"added"`
}

type Remote struct {
	Mode string `json:"mode"` // off | tailnet | tailscale-serve
	Addr string `json:"addr,omitempty"`
}

type Settings struct {
	Lang                  string `json:"lang"`
	Remote                Remote `json:"remote"`
	DefaultPermissionMode string `json:"default_permission_mode"`
}

var (
	Langs       = []string{"it", "en"}
	RemoteModes = []string{"off", "tailnet", "tailscale-serve"}
)

func initState(st *State) {
	st.Version = 1
	if st.Projects == nil {
		st.Projects = []Project{}
	}
	if st.Machines == nil {
		st.Machines = []Machine{}
	}
	if st.Settings.Lang == "" {
		st.Settings.Lang = "it"
	}
	if st.Settings.Remote.Mode == "" {
		st.Settings.Remote.Mode = "off"
	}
	if st.Settings.DefaultPermissionMode == "" {
		st.Settings.DefaultPermissionMode = "default"
	}
}

// ProjectID is stable for a root: p_ and 8 hex of its sha256.
func ProjectID(root string) string {
	h := sha256.Sum256([]byte(filepath.Clean(root)))
	return "p_" + hex.EncodeToString(h[:])[:8]
}

func (s *Store) State() (*State, error) { return load(s, stateFile, initState) }

func (s *Store) UpdateState(fn func(*State) error) error {
	return update(s, stateFile, initState, fn)
}

func (st *State) Machine(id string) (*Machine, bool) {
	for i := range st.Machines {
		if st.Machines[i].ID == id {
			return &st.Machines[i], true
		}
	}
	return nil, false
}

var sshTargetRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@-]{0,252}$`)

// ValidSSHTarget accepts a host alias, name or user@host as written in ~/.ssh/config, and
// nothing ssh would read as an option or a command.
func ValidSSHTarget(t string) bool { return sshTargetRe.MatchString(t) }

// AddMachine registers an ssh target once.
func (s *Store) AddMachine(m Machine) (Machine, error) {
	if !ValidSSHTarget(m.SSHTarget) {
		return Machine{}, fmt.Errorf("invalid ssh target %q", m.SSHTarget)
	}
	if m.Label = strings.TrimSpace(m.Label); m.Label == "" {
		m.Label = m.SSHTarget
	}
	m.ID, m.Added = NewID("m_")[:10], now()
	err := s.UpdateState(func(st *State) error {
		for _, x := range st.Machines {
			if x.SSHTarget == m.SSHTarget {
				return fmt.Errorf("%s: %w", m.SSHTarget, ErrExists)
			}
		}
		st.Machines = append(st.Machines, m)
		return nil
	})
	return m, err
}

func (s *Store) RemoveMachine(id string) error {
	return s.UpdateState(func(st *State) error {
		for i := range st.Machines {
			if st.Machines[i].ID == id {
				st.Machines = append(st.Machines[:i], st.Machines[i+1:]...)
				return nil
			}
		}
		return ErrNotFound
	})
}

func (st *State) Project(id string) (*Project, bool) {
	for i := range st.Projects {
		if st.Projects[i].ID == id {
			return &st.Projects[i], true
		}
	}
	return nil, false
}

var ErrExists = errors.New("already registered")

// AddProject registers root under client; name defaults to the directory name.
func (s *Store) AddProject(root, client, name string) (Project, error) {
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) {
		return Project{}, fmt.Errorf("%s: not an absolute path", root)
	}
	if name = strings.TrimSpace(name); name == "" {
		name = filepath.Base(root)
	}
	p := Project{ID: ProjectID(root), Root: root, Client: strings.TrimSpace(client), Name: name, Added: now()}
	err := s.UpdateState(func(st *State) error {
		if _, ok := st.Project(p.ID); ok {
			return fmt.Errorf("%s: %w", root, ErrExists)
		}
		st.Projects = append(st.Projects, p)
		return nil
	})
	return p, err
}

func (s *Store) RemoveProject(id string) error {
	return s.UpdateState(func(st *State) error {
		for i := range st.Projects {
			if st.Projects[i].ID == id {
				st.Projects = append(st.Projects[:i], st.Projects[i+1:]...)
				return nil
			}
		}
		return ErrNotFound
	})
}

var ErrNotFound = errors.New("not found")

func ValidateSettings(v Settings) error {
	if !contains(Langs, v.Lang) {
		return fmt.Errorf("lang must be one of %s", strings.Join(Langs, ", "))
	}
	if !contains(RemoteModes, v.Remote.Mode) {
		return fmt.Errorf("remote.mode must be one of %s", strings.Join(RemoteModes, ", "))
	}
	if !contains(PermissionModes, v.DefaultPermissionMode) {
		return fmt.Errorf("default_permission_mode must be one of %s", strings.Join(PermissionModes, ", "))
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
