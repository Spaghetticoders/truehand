//go:build unix

package appstate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

var ErrLocked = errors.New("another truehand serve is already running")

// Lock takes serve.lock for the life of the process and records the pid in it. The kernel
// drops the lock when the process dies, so a stale file never blocks a restart.
func (s *Store) Lock() (release func(), err error) {
	p := filepath.Join(s.Dir, "serve.lock")
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		b, _ := os.ReadFile(p)
		f.Close()
		if pid := strings.TrimSpace(string(b)); pid != "" {
			return nil, fmt.Errorf("%w (pid %s)", ErrLocked, pid)
		}
		return nil, ErrLocked
	}
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	return func() {
		_ = f.Truncate(0)
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// flockFile holds an exclusive lock on path (created 0600) until the returned func runs: a
// read-change-write of a state file is one step even across processes.
func flockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
