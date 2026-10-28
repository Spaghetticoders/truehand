package agents

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/spaghetticoders/truehand/internal/appstate"
)

// Proc is a running session: its process group, terminal and viewers.
type Proc struct {
	m          *Manager
	secretHash string
	cmd        *exec.Cmd
	pty        *os.File

	mu         sync.Mutex
	rec        appstate.Session
	ring       *ring
	log        *os.File
	logSize    int64
	subs       map[chan []byte]struct{}
	cols, rows uint16
	closed     bool
	stopping   bool

	readDone chan struct{}
	done     chan struct{}
}

func (p *Proc) Session() appstate.Session {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rec
}

func (p *Proc) Size() (cols, rows uint16) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cols, p.rows
}

// Done is closed when the process has exited and its state is recorded.
func (p *Proc) Done() <-chan struct{} { return p.done }

// Subscribe returns the scrollback so far and a channel of everything after it, with no gap
// and no overlap. A viewer that falls behind is dropped (its channel closes) rather than
// slowing the agent; it reconnects and gets the scrollback again.
func (p *Proc) Subscribe() (history []byte, ch <-chan []byte, cancel func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	history = p.ring.Bytes()
	c := make(chan []byte, 512)
	if p.closed {
		close(c)
		return history, c, func() {}
	}
	p.subs[c] = struct{}{}
	return history, c, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if _, ok := p.subs[c]; ok {
			delete(p.subs, c)
			close(c)
		}
	}
}

var ErrClosed = errors.New("the session has ended")

// Write types into the session's terminal.
func (p *Proc) Write(b []byte) error {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return ErrClosed
	}
	_, err := p.pty.Write(b)
	return err
}

// Resize sets the terminal size; the last viewer to type decides it.
func (p *Proc) Resize(cols, rows uint16) error {
	if cols == 0 || rows == 0 || cols > 1000 || rows > 1000 {
		return errors.New("invalid terminal size")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrClosed
	}
	if err := pty.Setsize(p.pty, &pty.Winsize{Cols: cols, Rows: rows}); err != nil {
		return err
	}
	p.cols, p.rows = cols, rows
	return nil
}

func (p *Proc) read() {
	defer close(p.readDone)
	buf := make([]byte, 32<<10)
	for {
		n, err := p.pty.Read(buf)
		if n > 0 {
			p.emit(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (p *Proc) emit(b []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ring.Write(b)
	if n, err := p.log.Write(b); err == nil {
		p.logSize += int64(n)
	}
	if p.logSize > 2*int64(len(p.ring.buf)) {
		if err := p.log.Truncate(0); err == nil {
			kept := p.ring.Bytes()
			if n, err := p.log.Write(kept); err == nil {
				p.logSize = int64(n)
			}
		}
	}
	for c := range p.subs {
		select {
		case c <- append([]byte(nil), b...):
		default:
			delete(p.subs, c)
			close(c)
		}
	}
}

func (p *Proc) setPrompted() {
	p.mu.Lock()
	done := p.rec.Prompted
	p.rec.Prompted = true
	rec := p.rec
	p.mu.Unlock()
	if !done {
		_ = p.m.cfg.Store.PutSession(rec)
	}
}

func (p *Proc) setStatus(status string) {
	p.mu.Lock()
	if p.rec.Status == status {
		p.mu.Unlock()
		return
	}
	p.rec.Status = status
	p.rec.Updated = time.Now().UTC().Truncate(time.Second)
	rec := p.rec
	p.mu.Unlock()
	_ = p.m.cfg.Store.PutSession(rec)
	p.m.changed(rec)
}

func (p *Proc) terminate() {
	p.mu.Lock()
	p.stopping = true
	pid := p.rec.PID
	p.mu.Unlock()
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	go func() {
		select {
		case <-p.done:
		case <-time.After(p.m.cfg.KillGrace):
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	}()
}

// killLeftovers kills what is left of a session's process group after its leader exited.
// The leader is reaped by then, so a live process with pid == pgid means the number was
// reused by someone else, whose group this may now be: then nothing is killed.
func killLeftovers(pgid int) {
	if syscall.Kill(pgid, 0) != syscall.ESRCH {
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

func (p *Proc) wait() {
	err := p.cmd.Wait()
	code := 0
	if err != nil {
		code = -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				code = 128 + int(ws.Signal())
			} else {
				code = ee.ExitCode()
			}
		}
	}
	// Whatever the agent left behind in its group goes with it, by force if it ignores TERM.
	_ = syscall.Kill(-p.rec.PID, syscall.SIGTERM)
	go func(pgid int) {
		time.Sleep(p.m.cfg.KillGrace)
		killLeftovers(pgid)
	}(p.rec.PID)
	select {
	case <-p.readDone:
	case <-time.After(500 * time.Millisecond):
	}
	p.pty.Close()
	<-p.readDone

	p.mu.Lock()
	p.closed = true
	for c := range p.subs {
		delete(p.subs, c)
		close(c)
	}
	p.log.Close()
	t := time.Now().UTC().Truncate(time.Second)
	p.rec.Ended, p.rec.ExitCode, p.rec.Updated = &t, &code, t
	p.rec.Status = appstate.StatusExited
	if code != 0 && !p.stopping {
		p.rec.Status = appstate.StatusCrashed
	}
	p.rec.Resumable = p.rec.ClaudeSessionID != "" && p.rec.Prompted
	p.rec.Stopped = p.stopping
	rec := p.rec
	p.mu.Unlock()

	_ = p.m.cfg.Store.PutSession(rec)
	p.m.mu.Lock()
	delete(p.m.procs, rec.ID)
	p.m.mu.Unlock()
	close(p.done)
	p.m.changed(rec)
}
