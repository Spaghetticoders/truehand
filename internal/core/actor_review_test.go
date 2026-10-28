package core

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func stubProcs(t *testing.T, ps map[int]string, exe map[int]string) {
	t.Helper()
	origPS, origExe := psLine, exePath
	t.Cleanup(func() { psLine, exePath = origPS, origExe })
	psLine = func(pid int) (string, error) {
		if l, ok := ps[pid]; ok {
			return l, nil
		}
		return "", errors.New("no such process")
	}
	exePath = func(pid int) string { return exe[pid] }
}

func TestReviewClaudeAncestorHermetic(t *testing.T) {
	start := os.Getppid()
	stubProcs(t, map[int]string{start: "500 bash", 500: "400 2.1.301", 400: "1 node"},
		map[int]string{500: "/home/e/.local/share/claude/versions/2.1.301"})
	if got := claudeAncestor(); got != 500 {
		t.Fatalf("got %d, want 500", got)
	}
}

func TestReviewClaudeAncestorNearestWins(t *testing.T) {
	start := os.Getppid()
	stubProcs(t, map[int]string{start: "700 bash", 700: "600 claude", 600: "1 claude"}, nil)
	if got := claudeAncestor(); got != 700 {
		t.Fatalf("got %d, want the nearest session 700", got)
	}
}

func TestReviewClaudeAncestorStopsOnPSErrorAndDepth(t *testing.T) {
	start := os.Getppid()
	stubProcs(t, map[int]string{start: "900 bash"}, nil)
	if got := claudeAncestor(); got != 0 {
		t.Fatalf("got %d", got)
	}
	chain := map[int]string{}
	pid := start
	for i := 0; i < 20; i++ {
		next := 10000 + i
		chain[pid] = strconv.Itoa(next) + " bash"
		pid = next
	}
	chain[pid] = "1 claude"
	stubProcs(t, chain, nil)
	if got := claudeAncestor(); got != 0 {
		t.Fatalf("walked past the depth limit: %d", got)
	}
}

func TestReviewIsClaudeLookalikes(t *testing.T) {
	for comm, want := range map[string]bool{
		"/Users/e/Library/Application Support/claude/versions/2.1.3": true,
		"/x/claude/versions/2.1.301 (deleted)":                       false,
		"/x/notclaude/versions/2.1.301":                              false,
		"/x/claude/versions/2.1.301/claude-helper":                   false,
		"/x/claude/versions/v2.1.301":                                false,
		"/x/claude/versions/2.1.301;rm":                              false,
		"/x/claude/versions/..":                                      false,
	} {
		if got := isClaude(comm); got != want {
			t.Errorf("isClaude(%q) = %v, want %v", comm, got, want)
		}
	}
}

func stubArgs(t *testing.T, args map[int]string) {
	t.Helper()
	orig := argsOf
	t.Cleanup(func() { argsOf = orig })
	argsOf = func(pid int) string { return args[pid] }
}

func TestReviewNpmClaudeThroughNode(t *testing.T) {
	start := os.Getppid()
	cli := "/usr/local/lib/node_modules/@anthropic-ai/claude-code/cli.js"
	stubProcs(t, map[int]string{start: "800 bash", 800: "1 node"}, nil)
	stubArgs(t, map[int]string{800: "node --no-warnings " + cli + " --resume x"})
	if got := claudeAncestor(); got != 800 {
		t.Fatalf("npm claude under node: got %d", got)
	}
	stubArgs(t, map[int]string{800: "node /usr/local/lib/node_modules/@anthropic-ai/claude-code-ui/cli.js"})
	if got := claudeAncestor(); got != 0 {
		t.Fatalf("lookalike package matched: %d", got)
	}
	stubArgs(t, map[int]string{800: "node /tmp/x.js " + cli})
	if got := claudeAncestor(); got != 0 {
		t.Fatalf("cli.js as a later argument of another script matched: %d", got)
	}
	stubArgs(t, map[int]string{800: "node x.js " + cli})
	if got := claudeAncestor(); got != 0 {
		t.Fatalf("cli.js after another relative script matched: %d", got)
	}
	stubProcs(t, map[int]string{start: "800 bash", 800: "1 python3"}, nil)
	stubArgs(t, map[int]string{800: "python3 " + cli})
	if got := claudeAncestor(); got != 0 {
		t.Fatalf("non-interpreter matched: %d", got)
	}
}

func TestReviewNpmBinSymlink(t *testing.T) {
	dir := t.TempDir()
	cli := filepath.Join(dir, "lib", "node_modules", "@anthropic-ai", "claude-code", "cli.js")
	os.MkdirAll(filepath.Dir(cli), 0o755)
	os.WriteFile(cli, []byte("#!/usr/bin/env node\n"), 0o755)
	bin := filepath.Join(dir, "bin", "claude")
	os.MkdirAll(filepath.Dir(bin), 0o755)
	os.Symlink("../lib/node_modules/@anthropic-ai/claude-code/cli.js", bin)
	other := filepath.Join(dir, "bin", "claude-other")
	os.WriteFile(filepath.Join(dir, "x.js"), nil, 0o644)
	os.Symlink(filepath.Join(dir, "x.js"), other)
	if !isClaudeScript(bin) {
		t.Fatal("npm bin link not followed")
	}
	t.Chdir(dir)
	if isClaudeScript(other) || isClaudeScript("bin/claude") {
		t.Fatal("unrelated or relative link matched")
	}
}
