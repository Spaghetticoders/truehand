package board

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

// Save writes task.yaml. Fields this version does not know, written by a newer one, are kept:
// an older binary moving a task must not drop what a newer one recorded on it.
func (s *Store) Save(t *Task) error {
	p := filepath.Join(s.TaskDir(t.ID), "task.yaml")
	var doc yaml.Node
	if err := doc.Encode(t); err != nil {
		return err
	}
	if old, err := os.ReadFile(p); err == nil {
		keepUnknown(&doc, old)
	}
	b, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	return writeAtomic(p, b)
}

// keepUnknown appends to the mapping doc the top-level entries of old whose keys Task does not
// declare.
func keepUnknown(doc *yaml.Node, old []byte) {
	var prev yaml.Node
	if yaml.Unmarshal(old, &prev) != nil || len(prev.Content) != 1 || prev.Content[0].Kind != yaml.MappingNode || doc.Kind != yaml.MappingNode {
		return
	}
	known := taskFields()
	m := prev.Content[0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		if k := m.Content[i].Value; !known[k] {
			doc.Content = append(doc.Content, m.Content[i], m.Content[i+1])
		}
	}
}

func taskFields() map[string]bool {
	known := map[string]bool{}
	rt := reflect.TypeOf(Task{})
	for i := 0; i < rt.NumField(); i++ {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("yaml"), ",")
		if name == "" {
			name = strings.ToLower(rt.Field(i).Name)
		}
		known[name] = true
	}
	return known
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

// DescriptionPath is the task's own brief: the ticket when no external tracker is used.
func (s *Store) DescriptionPath(id string) string {
	return filepath.Join(s.TaskDir(id), "description.md")
}

func (s *Store) Description(id string) (string, error) {
	b, err := os.ReadFile(s.DescriptionPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

func (s *Store) SetDescription(id, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		if err := os.Remove(s.DescriptionPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeAtomic(s.DescriptionPath(id), []byte(text+"\n"))
}

// NextID returns prefix-N with N one above the highest number already used with that prefix.
func (s *Store) NextID(prefix string) (string, error) {
	entries, err := os.ReadDir(s.TasksDir())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	n := 0
	for _, e := range entries {
		rest, ok := strings.CutPrefix(e.Name(), prefix+"-")
		if !ok {
			continue
		}
		var k int
		if _, err := fmt.Sscanf(rest, "%d", &k); err == nil && fmt.Sprint(k) == rest && k > n {
			n = k
		}
	}
	return fmt.Sprintf("%s-%d", prefix, n+1), nil
}
