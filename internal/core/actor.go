package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// AgentDetector returns the pid of the agent session the process runs in, or 0. Tests
// replace it; agents cannot, since it is not reachable from the command line.
var AgentDetector = claudeAncestor

// claudeAncestor returns the pid of the nearest ancestor process that is Claude Code, or 0.
// Bash tool calls, hooks and MCP servers of one Claude Code session all descend from it,
// which gives them one identity.
func claudeAncestor() int { return AgentAncestorOf(os.Getppid()) }

// AgentAncestorOf returns the pid of the Claude Code process that pid is or descends from,
// or 0: the same walk AgentDetector does for this process, for another one (a socket peer).
func AgentAncestorOf(pid int) int {
	for i := 0; i < 12 && pid > 1; i++ {
		out, err := psLine(pid)
		if err != nil {
			return 0
		}
		f := strings.Fields(out)
		if len(f) < 2 {
			return 0
		}
		comm := strings.Join(f[1:], " ")
		if isClaude(comm) || isClaude(exePath(pid)) || interpreters[filepath.Base(comm)] && isClaudeScript(scriptArg(argsOf(pid))) {
			return pid
		}
		pid, _ = strconv.Atoi(f[0])
	}
	return 0
}

// psLine returns "ppid comm" of a process. Tests replace it.
var psLine = func(pid int) (string, error) {
	out, err := exec.Command("ps", "-o", "ppid=,comm=", "-p", strconv.Itoa(pid)).Output()
	return string(out), err
}

// exePath is the executable of a process where /proc has it. On Linux comm is only the
// executable's basename, "2.1.301" for the SDK's binary, so the path must come from here.
// An update that prunes old versions leaves running sessions on a "(deleted)" executable.
var exePath = func(pid int) string {
	p, _ := readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	return strings.TrimSuffix(p, " (deleted)")
}

var readlink = os.Readlink

// argsOf returns the command line of a process. Tests replace it.
var argsOf = func(pid int) string {
	out, _ := exec.Command("ps", "-o", "args=", "-p", strconv.Itoa(pid)).Output()
	return strings.TrimSpace(string(out))
}

// An npm install of Claude Code is a script: ps shows the interpreter running it.
var interpreters = map[string]bool{"node": true, "bun": true, "sh": true, "bash": true, "zsh": true, "dash": true}

// scriptArg is the first argument after the interpreter that is not an option. A script
// path with spaces in it is not recognised: ps joins arguments with spaces.
func scriptArg(args string) string {
	f := strings.Fields(args)
	for i := 1; i < len(f); i++ {
		if !strings.HasPrefix(f[i], "-") {
			return f[i]
		}
	}
	return ""
}

var claudeScriptRe = regexp.MustCompile(`/node_modules/@anthropic-ai/claude-code/cli\.m?js$`)

// isClaudeScript matches the npm package's cli.js, directly or through its `claude` bin link.
// A relative link is not followed: it is relative to the other process's directory.
func isClaudeScript(path string) bool {
	if claudeScriptRe.MatchString(filepath.ToSlash(path)) {
		return true
	}
	if !filepath.IsAbs(path) {
		return false
	}
	real, err := filepath.EvalSymlinks(path)
	return err == nil && claudeScriptRe.MatchString(filepath.ToSlash(real))
}

var versionRe = regexp.MustCompile(`^\d+(\.\d+)+([-+][0-9A-Za-z.-]+)?$`)

// isClaude matches the claude command, and the versioned binary the SDK and the native
// installer run directly (.../claude/versions/2.1.301), whose comm is that path.
func isClaude(comm string) bool {
	if filepath.Base(comm) == "claude" {
		return true
	}
	dir := filepath.Dir(comm)
	return versionRe.MatchString(filepath.Base(comm)) && filepath.Base(dir) == "versions" && filepath.Base(filepath.Dir(dir)) == "claude"
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

// PeerIsAgent reports whether another process (a socket peer) may be an agent: it descends
// from Claude Code, or it or an ancestor carries SessionEnv (an agent detached with setsid
// keeps it), or its own environment cannot be read. macOS hides the environment of its
// platform binaries (sh, curl, nc) and returns it empty; a real process always has some, so
// empty is doubt too. An agent can still strip its environment and connect with its own
// binary: this refuses the honest cases, the human factor stays the barrier.
func PeerIsAgent(pid int) bool {
	if pid <= 1 || AgentAncestorOf(pid) != 0 {
		return true
	}
	env, ok := procEnv(pid)
	if !ok || len(env) == 0 || hasSessionEnv(env) {
		return true
	}
	for i, p := 0, parentOf(pid); i < 12 && p > 1; i, p = i+1, parentOf(p) {
		if env, ok := procEnv(p); ok && hasSessionEnv(env) {
			return true
		}
	}
	return false
}

func hasSessionEnv(env []string) bool {
	for _, kv := range env {
		if strings.HasPrefix(kv, SessionEnv+"=") {
			return true
		}
	}
	return false
}

func parentOf(pid int) int {
	out, err := psLine(pid)
	if err != nil {
		return 0
	}
	f := strings.Fields(out)
	if len(f) == 0 {
		return 0
	}
	p, _ := strconv.Atoi(f[0])
	return p
}

// procEnv returns another process's environment, and false when it cannot be read. Tests
// replace it.
var procEnv = processEnv

// SessionEnv is set in the environment of every agent the app launches. setsid or nohup
// detach a process from the claude ancestor AgentDetector looks for, but keep it.
const SessionEnv = "TRUEHAND_SESSION"

// InAgent reports whether this process runs inside an agent session.
func InAgent() bool { return AgentDetector() != 0 || os.Getenv(SessionEnv) != "" }

// InAgentSession reports whether this process runs inside an agent session.
func (w *Workspace) InAgentSession() bool { return InAgent() }

func (w *Workspace) agentActor() string {
	pid := AgentDetector()
	if pid == 0 {
		if os.Getenv(SessionEnv) != "" {
			return "claude:pid-unknown"
		}
		return ""
	}
	if dir := w.sessionsDir(); dir != "" {
		if b, err := os.ReadFile(filepath.Join(dir, strconv.Itoa(pid))); err == nil && len(b) > 0 {
			return "claude:" + strings.TrimSpace(string(b))
		}
	}
	return "claude:pid-" + strconv.Itoa(pid)
}
