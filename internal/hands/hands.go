// Package hands implements Truehand's deterministic operations. Every hand returns a
// Result and records it as evidence on the task.
package hands

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/evidence"
)

// Result is the contract shared by all hands (CLI --json, MCP tools).
type Result struct {
	Hand     string          `json:"hand"`
	Task     string          `json:"task"`
	Record   string          `json:"record"`
	Verdict  string          `json:"verdict"`
	Summary  string          `json:"summary"`
	Evidence []evidence.File `json:"evidence"`
	Next     string          `json:"next,omitempty"`
}

// Session wraps one hand run: it fingerprints the content before the hand works and refuses
// to record a pass if the content changed underneath.
type Session struct {
	WS     *core.Workspace
	Task   *board.Task
	Log    evidence.Log
	Rec    *evidence.Record
	Base   string
	before evidence.Fingerprint
}

func Begin(ws *core.Workspace, t *board.Task, hand, key string) (*Session, error) {
	log := evidence.Log{Store: ws.Store, Task: t.ID}
	id, err := log.NewID(hand)
	if err != nil {
		return nil, err
	}
	base, err := ws.BaseOf(t)
	if err != nil {
		return nil, err
	}
	fp, err := evidence.Compute(ws.Repo, base)
	if err != nil {
		return nil, err
	}
	return &Session{
		WS: ws, Task: t, Log: log, before: fp, Base: base,
		Rec: &evidence.Record{ID: id, Hand: hand, Key: key, Actor: ws.Actor(), Inputs: fp},
	}, nil
}

// File returns the absolute path for a new evidence file and registers it on the record.
func (s *Session) File(name string) string {
	abs, rel := s.Log.FilePath(s.Rec.ID, name)
	s.Rec.Files = append(s.Rec.Files, evidence.File{Path: rel})
	return abs
}

// WriteFile writes and registers an evidence file.
func (s *Session) WriteFile(name string, b []byte) error {
	p := s.File(name)
	if err := os.MkdirAll(s.Log.Dir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

// Finish records the evidence. A pass is downgraded to error if the task content changed
// while the hand ran, because the evidence would describe content that no longer exists.
func (s *Session) Finish(verdict, summary, next string) (*Result, error) {
	if verdict == evidence.Pass {
		after, err := evidence.Compute(s.WS.Repo, s.Base)
		if err != nil {
			return nil, err
		}
		if stale := s.before.Stale(after); len(stale) > 0 {
			verdict = evidence.Error
			summary = "content changed while the hand ran (" + strings.Join(stale, ", ") + "); run it again"
		}
	}
	s.Rec.Verdict, s.Rec.Summary = verdict, summary
	if err := s.Log.Append(s.Rec); err != nil {
		return nil, err
	}
	_ = s.WS.Store.AppendEvent(s.Task.ID, board.Event{
		Type: "hand", Actor: s.Rec.Actor,
		Detail: map[string]any{"hand": s.Rec.Hand, "record": s.Rec.ID, "verdict": verdict, "summary": summary},
	})
	return &Result{
		Hand: s.Rec.Hand, Task: s.Task.ID, Record: s.Rec.ID, Verdict: verdict,
		Summary: summary, Evidence: s.Rec.Files, Next: next,
	}, nil
}

// CmdResult is the outcome of running a shell command.
type CmdResult struct {
	ExitCode int
	Output   []byte
	Duration time.Duration
	TimedOut bool
}

// RunShell runs a command line through the user's shell in dir, capturing combined output.
func RunShell(ctx context.Context, dir, cmdline string, timeout time.Duration, tee io.Writer) (CmdResult, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", cmdline)
	cmd.Dir = dir
	// Own process group, killed as a whole: a test runner's children would otherwise keep the
	// output pipe open and outlive the timeout.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	var buf bytes.Buffer
	var w io.Writer = &buf
	if tee != nil {
		w = io.MultiWriter(&buf, tee)
	}
	cmd.Stdout, cmd.Stderr = w, w
	start := time.Now()
	err := cmd.Run()
	res := CmdResult{Output: buf.Bytes(), Duration: time.Since(start)}
	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		res.ExitCode = -1
		return res, nil
	}
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		res.ExitCode = exit.ExitCode()
	default:
		return res, fmt.Errorf("run %q: %w", cmdline, err)
	}
	return res, nil
}

func tail(b []byte, lines int) string {
	s := strings.TrimRight(string(b), "\n")
	parts := strings.Split(s, "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n")
}

func sortStrings(s []string) { sort.Strings(s) }
