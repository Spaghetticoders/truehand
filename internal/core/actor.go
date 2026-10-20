package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// AgentDetector returns the pid of the agent session the process runs in, or 0. Tests
// replace it; agents cannot, since it is not reachable from the command line.
var AgentDetector = claudeAncestor

// claudeAncestor returns the pid of the nearest ancestor process named "claude", or 0.
// Bash tool calls, hooks and MCP servers of one Claude Code session all descend from it,
// which gives them one identity.
func claudeAncestor() int {
	pid := os.Getppid()
	for i := 0; i < 12 && pid > 1; i++ {
		out, err := exec.Command("ps", "-o", "ppid=,comm=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			return 0
		}
		f := strings.Fields(string(out))
		if len(f) < 2 {
			return 0
		}
		if filepath.Base(strings.Join(f[1:], " ")) == "claude" {
			return pid
		}
		pid, _ = strconv.Atoi(f[0])
	}
	return 0
}

func (w *Workspace) sessionsDir() string {
	gd, err := w.Repo.GitDir()
	if err != nil {
		return ""
	}
	return filepath.Join(gd, "truehand", "sessions")
}

// RecordSession maps the calling Claude Code process to its session id (SessionStart hook).
func (w *Workspace) RecordSession(sessionID string) {
	pid := AgentDetector()
	dir := w.sessionsDir()
	if pid == 0 || dir == "" || sessionID == "" {
		return
	}
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)), []byte(sessionID), 0o644)
}

// InAgentSession reports whether this process runs inside an agent session.
func (w *Workspace) InAgentSession() bool { return AgentDetector() != 0 }

func (w *Workspace) agentActor() string {
	pid := AgentDetector()
	if pid == 0 {
		return ""
	}
	if dir := w.sessionsDir(); dir != "" {
		if b, err := os.ReadFile(filepath.Join(dir, strconv.Itoa(pid))); err == nil && len(b) > 0 {
			return "claude:" + strings.TrimSpace(string(b))
		}
	}
	return "claude:pid-" + strconv.Itoa(pid)
}
