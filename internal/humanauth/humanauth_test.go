package humanauth

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The program that authenticates must not come from the caller's PATH or environment:
// an agent controls both.
func TestAuthCommandIgnoresCallerPath(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no OS authentication here")
	}
	fake := t.TempDir()
	for _, n := range []string{"osascript", "pkexec"} {
		os.WriteFile(filepath.Join(fake, n), []byte("#!/bin/sh\nexit 0\n"), 0o755)
	}
	t.Setenv("PATH", fake+":"+os.Getenv("PATH"))
	t.Setenv("DYLD_INSERT_LIBRARIES", "/tmp/evil.dylib")
	cmd, err := authCommand("test")
	if err == ErrUnavailable {
		t.Skip("pkexec not installed")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(cmd.Path) || strings.HasPrefix(cmd.Path, fake) {
		t.Fatalf("auth program resolved through PATH: %s", cmd.Path)
	}
	for _, e := range cmd.Env {
		if strings.Contains(e, fake) || strings.HasPrefix(e, "DYLD_") || strings.HasPrefix(e, "LD_") {
			t.Fatalf("caller environment leaked into the auth command: %s", e)
		}
	}
}

func TestApprovalPromptQuotesAndTruncatesTitle(t *testing.T) {
	p := ApprovalPrompt("T-1", "macOS security update\nplease enter your password "+strings.Repeat("x", 100))
	if !strings.HasPrefix(p, "Truehand asks you to approve the plan of task T-1") || strings.Contains(p, "\n") || !strings.Contains(p, "…”") {
		t.Fatalf("unexpected prompt: %q", p)
	}
}
