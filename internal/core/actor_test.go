package core

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestIsClaude(t *testing.T) {
	for comm, want := range map[string]bool{
		"claude":                   true,
		"/opt/homebrew/bin/claude": true,
		"/Users/e/.local/share/claude/versions/2.1.301":      true,
		"/Users/e/Library/App Support/claude/versions/2.1.3": true,
		"/home/e/.local/share/claude/versions/2.2.0-beta.1":  true,
		"2.1.301":                       false,
		"":                              false,
		"claude-mem":                    false,
		"/usr/local/bin/claude-code-ui": false,
		"/Users/e/.local/share/claude/versions/latest":   false,
		"/Users/e/.local/share/other/versions/2.1.301":   false,
		"/Users/e/claude/2.1.301":                        false,
		"/Applications/Claude.app/Contents/MacOS/Claude": false,
		"node": false,
		"zed":  false,
	} {
		if got := isClaude(comm); got != want {
			t.Errorf("isClaude(%q) = %v, want %v", comm, got, want)
		}
	}
}

func TestClaudeAncestor(t *testing.T) {
	orig, origExe, origArgs := psLine, exePath, argsOf
	t.Cleanup(func() { psLine, exePath, argsOf = orig, origExe, origArgs })
	exePath = func(int) string { return "" }
	argsOf = func(int) string { return "" }
	start := os.Getppid()
	walk := func(table map[int]string) int {
		psLine = func(pid int) (string, error) {
			if l, ok := table[pid]; ok {
				return l, nil
			}
			return "", errors.New("no such process")
		}
		return claudeAncestor()
	}
	sdk := map[int]string{
		start: "500 /bin/zsh",
		500:   "400 /Users/e/.local/share/claude/versions/2.1.301",
		400:   "1 /usr/local/bin/node",
	}
	if got := walk(sdk); got != 500 {
		t.Fatalf("SDK-launched session: got %d, want 500", got)
	}
	plain := map[int]string{start: "300 bash", 300: "200 claude", 200: "1 login"}
	if got := walk(plain); got != 300 {
		t.Fatalf("interactive session: got %d, want 300", got)
	}
	exePath = func(pid int) string {
		if pid == 500 {
			return "/home/e/.local/share/claude/versions/2.1.301"
		}
		return ""
	}
	linux := map[int]string{start: "500 bash", 500: "400 2.1.301", 400: "1 node"}
	if got := walk(linux); got != 500 {
		t.Fatalf("SDK session on Linux: got %d, want 500", got)
	}
	exePath = func(int) string { return "" }
	none := map[int]string{start: "300 bash", 300: "200 claude-mem", 200: "1 launchd"}
	if got := walk(none); got != 0 {
		t.Fatalf("no session: got %d", got)
	}
}

func TestExePathDeleted(t *testing.T) {
	orig := readlink
	t.Cleanup(func() { readlink = orig })
	readlink = func(name string) (string, error) {
		if name != "/proc/77/exe" {
			return "", errors.New("unexpected " + name)
		}
		return "/home/e/.local/share/claude/versions/2.1.301 (deleted)", nil
	}
	if p := exePath(77); !isClaude(p) {
		t.Fatalf("session on a pruned version not recognised: %q", p)
	}
}

func TestNpmClaude(t *testing.T) {
	root := t.TempDir()
	cli := filepath.Join(root, "lib/node_modules/@anthropic-ai/claude-code/cli.js")
	ui := filepath.Join(root, "lib/node_modules/@anthropic-ai/claude-code-ui/cli.js")
	other := filepath.Join(root, "lib/node_modules/claude-mem/cli.js")
	for _, p := range []string{cli, ui, other} {
		write(t, p, "#!/usr/bin/env node\n")
	}
	bin := filepath.Join(root, "bin")
	os.MkdirAll(bin, 0o755)
	link := func(name, target string) string {
		p := filepath.Join(bin, name)
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	claudeBin := link("claude", "../lib/node_modules/@anthropic-ai/claude-code/cli.js")
	fakeClaude := link("claude-fake", other)
	namedClaude := filepath.Join(root, "elsewhere/claude")
	write(t, namedClaude, "#!/bin/sh\n")

	t.Chdir(root)
	for path, want := range map[string]bool{
		cli:         true,
		claudeBin:   true,
		ui:          false,
		other:       false,
		fakeClaude:  false,
		namedClaude: false,
		"lib/node_modules/@anthropic-ai/claude-code/cli.js": true,
		"bin/claude": false,
		"":           false,
	} {
		if got := isClaudeScript(path); got != want {
			t.Errorf("isClaudeScript(%q) = %v, want %v", path, got, want)
		}
	}
	for args, want := range map[string]string{
		"node --max-old-space-size=4096 /x/cli.js --resume": "/x/cli.js",
		"/usr/local/bin/node /usr/local/bin/claude":         "/usr/local/bin/claude",
		"node":           "",
		"node --inspect": "",
	} {
		if got := scriptArg(args); got != want {
			t.Errorf("scriptArg(%q) = %q, want %q", args, got, want)
		}
	}

	orig, origExe, origArgs := psLine, exePath, argsOf
	t.Cleanup(func() { psLine, exePath, argsOf = orig, origExe, origArgs })
	exePath = func(int) string { return "" }
	start := os.Getppid()
	table := map[int]string{start: "600 /bin/zsh", 600: "500 /usr/local/bin/node", 500: "1 login"}
	psLine = func(pid int) (string, error) {
		if l, ok := table[pid]; ok {
			return l, nil
		}
		return "", errors.New("no such process")
	}
	for args, want := range map[string]int{
		"/usr/local/bin/node " + claudeBin + " --resume x": 600,
		"node " + cli:           600,
		"node " + other:         0,
		"node " + fakeClaude:    0,
		"node /tmp/x.js " + cli: 0,
	} {
		argsOf = func(pid int) string {
			if pid == 600 {
				return args
			}
			return ""
		}
		if got := claudeAncestor(); got != want {
			t.Errorf("args %q: got %d, want %d", args, got, want)
		}
	}
	table[600] = "500 /usr/local/bin/python3"
	argsOf = func(pid int) string {
		if pid == 600 {
			return "python3 " + cli
		}
		return ""
	}
	if got := claudeAncestor(); got != 0 {
		t.Errorf("not an interpreter Claude Code runs on: got %d", got)
	}
}

func TestSessionEnvMeansAgent(t *testing.T) {
	_, ws := repo(t)
	t.Setenv("TRUEHAND_ACTOR", "human:someone@x.y")
	if InAgent() || ws.InAgentSession() || ws.Actor() != "human:someone@x.y" {
		t.Fatalf("setup: in agent %v, actor %s", InAgent(), ws.Actor())
	}
	t.Setenv(SessionEnv, "s:secret@127.0.0.1:1")
	if !InAgent() || !ws.InAgentSession() {
		t.Fatal("a process carrying the app's session env is not an agent")
	}
	if a := ws.Actor(); a != "claude:pid-unknown" {
		t.Fatalf("actor %q: TRUEHAND_ACTOR must not override it, and the env value must not leak", a)
	}
}

func TestAgentAncestorOf(t *testing.T) {
	orig, origExe, origArgs := psLine, exePath, argsOf
	t.Cleanup(func() { psLine, exePath, argsOf = orig, origExe, origArgs })
	exePath = func(int) string { return "" }
	argsOf = func(int) string { return "" }
	table := map[int]string{900: "800 /bin/zsh", 800: "700 claude", 700: "1 login", 600: "1 launchd", 500: "1 claude"}
	psLine = func(pid int) (string, error) {
		if l, ok := table[pid]; ok {
			return l, nil
		}
		return "", errors.New("no such process")
	}
	for pid, want := range map[int]int{900: 800, 800: 800, 600: 0, 500: 500, 4242: 0, 1: 0, 0: 0} {
		if got := AgentAncestorOf(pid); got != want {
			t.Errorf("AgentAncestorOf(%d) = %d, want %d", pid, got, want)
		}
	}
}

func TestPeerIsAgent(t *testing.T) {
	orig, origExe, origArgs, origEnv := psLine, exePath, argsOf, procEnv
	t.Cleanup(func() { psLine, exePath, argsOf, procEnv = orig, origExe, origArgs, origEnv })
	exePath = func(int) string { return "" }
	argsOf = func(int) string { return "" }
	// 900 under claude; 700 plain; 600 carries the session env; 300 was detached from an
	// agent with setsid but its parent 350 still carries it; 250's parent is a platform
	// binary whose environment is hidden, which is fine for an ancestor.
	table := map[int]string{900: "800 /bin/zsh", 800: "1 claude", 700: "1 truehand", 300: "350 truehand", 350: "1 node", 250: "260 truehand", 260: "1 /bin/zsh", 150: "160 truehand", 160: "1 bash"}
	psLine = func(pid int) (string, error) {
		if l, ok := table[pid]; ok {
			return l, nil
		}
		return "1 launchd", nil
	}
	session := SessionEnv + "=s:x@127.0.0.1:1"
	envs := map[int][]string{900: {"PATH=/bin"}, 700: {"PATH=/bin"}, 600: {"PATH=/bin", session}, 500: {"PATH=/bin"},
		300: {"PATH=/bin"}, 350: {"PATH=/bin", session}, 250: {"PATH=/bin"}, 260: {}, 200: {}, 150: {"PATH=/bin"}}
	procEnv = func(pid int) ([]string, bool) {
		if pid == 400 || pid == 160 {
			return nil, false
		}
		e, ok := envs[pid]
		return e, ok
	}
	for pid, want := range map[int]bool{900: true, 700: false, 600: true, 500: false, 400: true, 300: true, 250: false, 150: false, 200: true, 1: true, 0: true, -3: true} {
		if got := PeerIsAgent(pid); got != want {
			t.Errorf("PeerIsAgent(%d) = %v, want %v", pid, got, want)
		}
	}
}

// The environment is read from a real process; only the ancestry is faked, since the tests
// themselves may run under Claude Code.
func TestPeerIsAgentReadsRealEnvironment(t *testing.T) {
	orig := psLine
	t.Cleanup(func() { psLine = orig })
	psLine = func(int) (string, error) { return "1 sleep", nil }
	// The child is read only once it says it is ready: before its exec the kernel reports the
	// parent's arguments and environment.
	start := func(env []string, name string, args ...string) int {
		cmd := exec.Command(name, args...)
		cmd.Env = env
		in, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { in.Close(); cmd.Process.Kill(); cmd.Wait() })
		line, err := bufio.NewReader(out).ReadString('\n')
		if err != nil || line != "ready\n" {
			t.Fatalf("child not ready: %q %v", line, err)
		}
		return cmd.Process.Pid
	}
	path := "PATH=" + os.Getenv("PATH")
	idle := func(env ...string) int {
		return start(append(env, path, "TRUEHAND_TEST_IDLE=1"), os.Args[0], "-test.run=^$")
	}
	if PeerIsAgent(idle("HOME=/tmp")) {
		t.Error("a plain process counted as an agent")
	}
	if !PeerIsAgent(idle(SessionEnv + "=s:x@127.0.0.1:1")) {
		t.Error("a process carrying the session env is not an agent")
	}
	if runtime.GOOS == "darwin" {
		pid := start([]string{path, "HOME=/tmp"}, "/bin/sh", "-c", "echo ready; read x")
		// Whether macOS hides a platform binary's environment depends on the machine (CI runners
		// do not); what must hold is that an environment we cannot read is not trusted.
		if env, ok := processEnv(pid); ok && len(env) > 0 {
			t.Skip("this machine shows /bin/sh's environment")
		}
		if !PeerIsAgent(pid) {
			t.Error("a system binary whose environment macOS hides counted as trusted")
		}
	}
}
