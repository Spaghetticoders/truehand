// Package machines reaches other computers over the system ssh: it probes them, installs
// this binary in ~/.truehand/bin, starts their `truehand serve --peer`, and talks HTTP to it
// through `truehand peer-proxy`, with one multiplexed ssh connection per machine.
package machines

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ErrAuth means ssh could not log in without asking (BatchMode): keys or agent missing.
var ErrAuth = errors.New("ssh needs a key or an agent to log in without a password")

// SSH runs commands on targets with the system ssh, so ~/.ssh/config, the agent and
// Tailscale SSH all apply as in a terminal.
type SSH struct {
	Bin        string // ssh executable; tests point it at a local stand-in
	ControlDir string // ControlMaster sockets (0700)
	Timeout    time.Duration
}

func (c *SSH) args(target string, remote string) []string {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	args := []string{
		"-o", "BatchMode=yes",
		"-o", fmt.Sprintf("ConnectTimeout=%d", int(timeout.Seconds())),
		"-o", "ServerAliveInterval=15",
	}
	if c.ControlDir != "" {
		args = append(args,
			"-o", "ControlMaster=auto",
			"-o", "ControlPath="+c.ControlDir+"/%C",
			"-o", "ControlPersist=10m",
		)
	}
	return append(args, "-T", target, remote)
}

// LoginShell wraps a script for the remote login shell, where tools installed per user
// (claude through a version manager, Homebrew) are on PATH.
func LoginShell(script string) string {
	return `exec "${SHELL:-/bin/sh}" -lc ` + Quote(script)
}

// Quote quotes s for a POSIX shell.
func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (c *SSH) command(ctx context.Context, target, script string, login bool) (*exec.Cmd, error) {
	if c.ControlDir != "" {
		if err := os.MkdirAll(c.ControlDir, 0o700); err != nil {
			return nil, err
		}
	}
	if login {
		script = LoginShell(script)
	}
	return exec.CommandContext(ctx, c.Bin, c.args(target, script)...), nil
}

// Run runs script on target in its login shell and returns stdout.
func (c *SSH) Run(ctx context.Context, target, script string, stdin io.Reader) ([]byte, error) {
	cmd, err := c.command(ctx, target, script, true)
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stdin, cmd.Stderr = stdin, &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, sshError(target, err, stderr.String())
	}
	return out, nil
}

func sshError(target string, err error, stderr string) error {
	msg := strings.TrimSpace(stderr)
	if strings.Contains(msg, "Permission denied") || strings.Contains(msg, "Host key verification failed") {
		return fmt.Errorf("%s: %w (%s)", target, ErrAuth, msg)
	}
	if msg == "" {
		msg = err.Error()
	}
	return fmt.Errorf("%s: %s", target, msg)
}

// Conn starts script on target (not in a login shell: it must start fast) and returns its
// stdin/stdout as a connection.
func (c *SSH) Conn(ctx context.Context, target, script string) (net.Conn, error) {
	cmd, err := c.command(context.WithoutCancel(ctx), target, script, false)
	if err != nil {
		return nil, err
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &procConn{cmd: cmd, in: in, out: out, stderr: stderr, target: target}, nil
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.b.Len() > 8<<10 {
		return len(p), nil
	}
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// procConn is a net.Conn over a child process's stdio.
type procConn struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    io.ReadCloser
	stderr *lockedBuffer
	target string
	once   sync.Once
}

func (p *procConn) Read(b []byte) (int, error) {
	n, err := p.out.Read(b)
	if err == io.EOF && n == 0 {
		if msg := strings.TrimSpace(p.stderr.String()); msg != "" {
			return 0, sshError(p.target, err, msg)
		}
	}
	return n, err
}

func (p *procConn) Write(b []byte) (int, error) { return p.in.Write(b) }

func (p *procConn) Close() error {
	p.once.Do(func() {
		p.in.Close()
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		_ = p.cmd.Wait()
	})
	return nil
}

func (p *procConn) LocalAddr() net.Addr                { return pipeAddr(p.target) }
func (p *procConn) RemoteAddr() net.Addr               { return pipeAddr(p.target) }
func (p *procConn) SetDeadline(t time.Time) error      { return nil }
func (p *procConn) SetReadDeadline(t time.Time) error  { return nil }
func (p *procConn) SetWriteDeadline(t time.Time) error { return nil }

type pipeAddr string

func (a pipeAddr) Network() string { return "ssh" }
func (a pipeAddr) String() string  { return string(a) }
