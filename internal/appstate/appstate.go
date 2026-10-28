// Package appstate keeps the Truehand app's server-side state in ~/.truehand/app: the
// registered projects and settings, the agent sessions, and the hashed credentials. Every
// file is private to the user and replaced atomically.
package appstate

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

const (
	stateFile    = "state.json"
	sessionsFile = "sessions.json"
	authFile     = "auth.json"
)

// DefaultDir is ~/.truehand/app.
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".truehand", "app"), nil
}

type Store struct {
	Dir string

	mu    sync.Mutex
	files map[string]*sync.Mutex
}

// Open creates dir (0700) and tightens its mode if it already existed.
func Open(dir string) (*Store, error) {
	for _, d := range []string{dir, filepath.Join(dir, "scrollback"), filepath.Join(dir, "hooks")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
		if err := os.Chmod(d, 0o700); err != nil {
			return nil, err
		}
	}
	return &Store{Dir: dir}, nil
}

var sidRe = regexp.MustCompile(`^s_[0-9a-f]{1,32}$`)

// ScrollbackPath is where a session's output is kept; an id that is not a session id is
// refused, so it can never name a path outside the scrollback directory.
func (s *Store) ScrollbackPath(sid string) (string, error) {
	if !sidRe.MatchString(sid) {
		return "", fmt.Errorf("invalid session id %q", sid)
	}
	return filepath.Join(s.Dir, "scrollback", sid+".log"), nil
}

func (s *Store) HooksSettingsPath() string { return filepath.Join(s.Dir, "hooks", "settings.json") }

func (s *Store) lockFile(name string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.files == nil {
		s.files = map[string]*sync.Mutex{}
	}
	m, ok := s.files[name]
	if !ok {
		m = &sync.Mutex{}
		s.files[name] = m
	}
	return m
}

func read[T any](s *Store, name string, v *T) error {
	b, err := os.ReadFile(filepath.Join(s.Dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func load[T any](s *Store, name string, init func(*T)) (*T, error) {
	m := s.lockFile(name)
	m.Lock()
	defer m.Unlock()
	v := new(T)
	if err := read(s, name, v); err != nil {
		return nil, err
	}
	if init != nil {
		init(v)
	}
	return v, nil
}

// update reads, changes and writes one file under its mutex; nothing is written when fn fails.
func update[T any](s *Store, name string, init func(*T), fn func(*T) error) error {
	m := s.lockFile(name)
	m.Lock()
	defer m.Unlock()
	v := new(T)
	if err := read(s, name, v); err != nil {
		return err
	}
	if init != nil {
		init(v)
	}
	if err := fn(v); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(filepath.Join(s.Dir, name), append(b, '\n'))
}

// WriteFileAtomic writes b to a private temporary file next to path and renames it over path.
func WriteFileAtomic(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// NewSecret returns 32 random bytes, hex encoded.
func NewSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// HashSecret is how secrets are stored: never the secret itself.
func HashSecret(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

// MatchSecret compares secret with a stored hash in constant time; an empty hash never matches.
func MatchSecret(hash, secret string) bool {
	if hash == "" || secret == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(HashSecret(secret)), []byte(hash)) == 1
}

func now() time.Time { return time.Now().UTC().Truncate(time.Second) }
