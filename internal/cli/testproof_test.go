package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func proofSandbox(t *testing.T, testCmd string) *sandbox {
	s := newSandbox(t)
	s.write("lib/code.txt", "buggy\n")
	s.git("add", ".")
	s.git("commit", "-q", "-m", "chore: code")
	s.must("init", "--stack", "generic")
	s.write(".truehand/config.yaml", "version: 0\nbase: main\ncommands:\n  test: \""+testCmd+"\"\ngates:\n  verified:\n    - check: test_proof\n")
	s.must("task", "new", "T-P", "--title", "fix")
	return s
}

func read(t *testing.T, s *sandbox, rel string) string {
	b, err := os.ReadFile(filepath.Join(s.dir, rel))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func TestTestProofPassesAndRestores(t *testing.T) {
	s := proofSandbox(t, "sh test/check.sh")
	s.write("lib/code.txt", "fixed\n")
	s.write("lib/new.txt", "new\n")
	s.write("test/check.sh", "grep -q fixed lib/code.txt && test -f lib/new.txt\n")
	out := s.must("hand", "test-proof", "--quiet")
	if !strings.Contains(out, "fail without it") {
		t.Fatalf("expected proof, got:\n%s", out)
	}
	if read(t, s, "lib/code.txt") != "fixed\n" || read(t, s, "lib/new.txt") != "new\n" {
		t.Fatalf("files not restored: code=%q new=%q", read(t, s, "lib/code.txt"), read(t, s, "lib/new.txt"))
	}
	logs, _ := filepath.Glob(filepath.Join(s.dir, ".truehand/tasks/T-P/evidence/test-proof-1-*"))
	if len(logs) != 3 {
		t.Fatalf("expected 3 evidence files, got %v", logs)
	}
	s.must("task", "move", "approved")
	s.must("task", "move", "building")
	s.must("task", "move", "verified")
}

func TestTestProofRejectsTestsThatDoNotCatchTheChange(t *testing.T) {
	s := proofSandbox(t, "sh test/check.sh")
	s.write("lib/code.txt", "fixed\n")
	s.write("test/check.sh", "true\n")
	out := s.fails(3, "hand", "test-proof", "--quiet")
	if !strings.Contains(out, "do not catch it") {
		t.Fatalf("expected rejection, got:\n%s", out)
	}
	if read(t, s, "lib/code.txt") != "fixed\n" {
		t.Fatal("file not restored after failed proof")
	}
}

func TestTestProofNeedsPassingTests(t *testing.T) {
	s := proofSandbox(t, "sh test/check.sh")
	s.write("lib/code.txt", "fixed\n")
	s.write("test/check.sh", "false\n")
	out := s.fails(3, "hand", "test-proof", "--quiet")
	if !strings.Contains(out, "fail even with the change") {
		t.Fatalf("expected failure with change, got:\n%s", out)
	}
}

func TestTestProofOnlyTests(t *testing.T) {
	s := proofSandbox(t, "sh test/check.sh")
	s.write("test/check.sh", "true\n")
	out := s.fails(3, "hand", "test-proof", "--quiet")
	if !strings.Contains(out, "nothing to prove") {
		t.Fatalf("expected nothing to prove, got:\n%s", out)
	}
}

func TestTestProofInterruptedRunMustBeRestored(t *testing.T) {
	s := proofSandbox(t, "sh test/check.sh")
	s.write("lib/code.txt", "fixed\n")
	s.write("test/check.sh", "grep -q fixed lib/code.txt\n")
	// Simulate a crash mid-run: manifest present, file reverted.
	dir := filepath.Join(s.dir, ".git/truehand/restore")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "0.bak"), []byte("fixed\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"task":"T-P","entries":[{"path":"lib/code.txt","existed":true,"mode":420,"backup":"0.bak"}]}`), 0o600)
	s.write("lib/code.txt", "buggy\n")
	out := s.fails(1, "hand", "test-proof", "--quiet")
	if !strings.Contains(out, "--restore") {
		t.Fatalf("expected restore hint, got:\n%s", out)
	}
	s.must("hand", "test-proof", "--restore")
	if read(t, s, "lib/code.txt") != "fixed\n" {
		t.Fatal("restore did not put the file back")
	}
	s.must("hand", "test-proof", "--quiet")
}

func goProofSandbox(t *testing.T, base string) *sandbox {
	s := newSandbox(t)
	s.write("go.mod", "module calc\n\ngo 1.21\n")
	s.write("calc.go", base)
	s.git("add", ".")
	s.git("commit", "-q", "-m", "chore: calc")
	s.must("init", "--stack", "generic")
	s.write(".truehand/config.yaml", "version: 0\nbase: main\ncommands:\n  test: \"go test ./...\"\ngates: {}\n")
	s.must("task", "new", "T-G", "--title", "mul")
	return s
}

// The approver's case: a new function and an assertion-free test. Without the change the test
// does not compile, which proves nothing about behaviour.
func TestTestProofRejectsBuildFailureProof(t *testing.T) {
	s := goProofSandbox(t, "package calc\n\nfunc Add(a, b int) int { return a + b }\n")
	s.write("calc.go", "package calc\n\nfunc Add(a, b int) int { return a + b }\n\nfunc Mul(a, b int) int { return a + b }\n")
	s.write("calc_test.go", "package calc\n\nimport \"testing\"\n\nfunc TestMul(t *testing.T) { _ = Mul(2, 3) }\n")
	out := s.fails(3, "hand", "test-proof", "--quiet")
	if !strings.Contains(out, "do not compile") {
		t.Fatalf("a build failure must not count as proof:\n%s", out)
	}
	s.write(".truehand/config.yaml", "version: 0\nbase: main\ncommands:\n  test: \"go test ./...\"\ntests:\n  accept_build_failure: true\ngates: {}\n")
	s.must("hand", "test-proof", "--quiet")
}

func TestTestProofGoBehaviourChange(t *testing.T) {
	s := goProofSandbox(t, "package calc\n\nfunc Mul(a, b int) int { return a + b }\n")
	s.write("calc.go", "package calc\n\nfunc Mul(a, b int) int { return a * b }\n")
	s.write("calc_test.go", "package calc\n\nimport \"testing\"\n\nfunc TestMul(t *testing.T) {\n\tif Mul(2, 3) != 6 {\n\t\tt.Fatal(Mul(2, 3))\n\t}\n}\n")
	out := s.must("hand", "test-proof", "--quiet")
	if !strings.Contains(out, "fail without it") {
		t.Fatalf("an assertion that catches the bug is a proof:\n%s", out)
	}
}
