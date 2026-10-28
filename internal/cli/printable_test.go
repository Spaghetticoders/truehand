package cli

import (
	"strings"
	"testing"
)

// Text from the repo reaches the terminal without escape sequences or bidi controls.
func TestTaskShowStripsTerminalControls(t *testing.T) {
	s := newSandbox(t)
	s.must("init", "--stack", "generic")
	s.write(".truehand/config.yaml", testConfig)
	s.must("task", "new", "T-1", "--title", "Ti\x1b]0;pwned\x07tle", "--description", "line one\n\tindented \x1b[2Jclear‮flip")
	s.must("task", "comment", "T-1", "ask\x1b[31m red\rover")
	out := s.must("task", "show", "T-1")
	if strings.ContainsAny(out, "\x1b\x07\r‮") {
		t.Fatalf("control characters reach the terminal: %q", out)
	}
	for _, want := range []string{"Ti]0;pwnedtle", "line one\n  \tindented [2Jclearflip", "ask[31m redover"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
	if out := s.must("task", "list"); strings.ContainsAny(out, "\x1b\x07") {
		t.Fatalf("task list: %q", out)
	}
}

func TestApproveShowsHiddenCharacters(t *testing.T) {
	s := newSandbox(t)
	s.must("init", "--stack", "generic")
	s.write(".truehand/config.yaml", testConfig)
	s.must("task", "new", "T-1", "--title", "x")
	s.write(".truehand/tasks/T-1/plan.md", validPlan+"\x1b[8mhidden\x1b[0m​\n")
	withTTY(t, true)
	code, out := s.run("T-1\n", "approve")
	if code != 0 {
		t.Fatalf("approve: %d\n%s", code, out)
	}
	if strings.ContainsAny(out, "\x1b​") || !strings.Contains(out, "3 control or invisible character(s)") || !strings.Contains(out, "[8mhidden[0m") {
		t.Fatalf("approval prompt: %q", out)
	}
}
