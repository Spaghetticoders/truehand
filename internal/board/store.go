package board

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Store is the task directory of one repo: <root>/.truehand/tasks.
type Store struct {
	Root string // repo root
}

func (s *Store) TasksDir() string { return filepath.Join(s.Root, ".truehand", "tasks") }

func (s *Store) TaskDir(id string) string { return filepath.Join(s.TasksDir(), id) }

// Rel returns a path relative to the repo root, slash-separated.
func (s *Store) Rel(abs string) string {
	r, err := filepath.Rel(s.Root, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(r)
}

func (s *Store) Create(t *Task) error {
	if err := ValidID(t.ID); err != nil {
		return err
	}
	dir := s.TaskDir(t.ID)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("task %s already exists", t.ID)
	}
	if err := os.MkdirAll(filepath.Join(dir, "evidence"), 0o755); err != nil {
		return err
	}
	return s.Save(t)
}

func (s *Store) Save(t *Task) error {
	b, err := yaml.Marshal(t)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.TaskDir(t.ID), "task.yaml"), b)
}

func (s *Store) Load(id string) (*Task, error) {
	if err := ValidID(id); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(s.TaskDir(id), "task.yaml"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("task %s not found", id)
	}
	if err != nil {
		return nil, err
	}
	var t Task
	if err := yaml.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("task %s: %w", id, err)
	}
	return &t, nil
}

func (s *Store) List() ([]*Task, error) {
	entries, err := os.ReadDir(s.TasksDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var tasks []*Task
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		t, err := s.Load(e.Name())
		if err != nil {
			continue
		}
		tasks = append(tasks, t)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Created.Before(tasks[j].Created) })
	return tasks, nil
}

type Event struct {
	Time   time.Time      `json:"time"`
	Type   string         `json:"type"`
	Actor  string         `json:"actor,omitempty"`
	From   State          `json:"from,omitempty"`
	To     State          `json:"to,omitempty"`
	Detail map[string]any `json:"detail,omitempty"`
}

func (s *Store) AppendEvent(id string, e Event) error {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	return AppendJSONL(filepath.Join(s.TaskDir(id), "events.jsonl"), e)
}

func (s *Store) Events(id string) ([]Event, error) {
	var out []Event
	err := ReadJSONL(filepath.Join(s.TaskDir(id), "events.jsonl"), func(line []byte) error {
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			return err
		}
		out = append(out, e)
		return nil
	})
	return out, err
}

// AppendJSONL appends one JSON value as a line. O_APPEND keeps concurrent writers line-atomic
// for lines under PIPE_BUF on local filesystems.
func AppendJSONL(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ReadJSONL calls fn for each non-empty line. A missing file yields no lines.
func ReadJSONL(path string, fn func([]byte) error) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if err := fn([]byte(line)); err != nil {
			return fmt.Errorf("%s:%d: %w", path, n, err)
		}
	}
	return sc.Err()
}

func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// WriteFileAtomic is exported for other packages writing task files.
func WriteFileAtomic(path string, b []byte) error { return writeAtomic(path, b) }
