package board

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A repo ignoring *.log (Flutter's default .gitignore) must still commit evidence logs:
// CI reads each one against its hash (Wovio, 2026-11-09).
func TestEvidenceLogsAreNotIgnored(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0o644)
	s := &Store{Root: dir}
	if err := s.Create(&Task{ID: "T-1", Title: "t", State: Planned}); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(s.TaskDir("T-1"), "evidence", "test-proof-1-with-change.log")
	os.WriteFile(log, []byte("ok\n"), 0o644)
	if out, err := exec.Command("git", "-C", dir, "check-ignore", "-q", log).CombinedOutput(); err == nil {
		t.Fatalf("evidence log is ignored by git: %s", out)
	}
	other := filepath.Join(dir, "build.log")
	os.WriteFile(other, nil, 0o644)
	if err := exec.Command("git", "-C", dir, "check-ignore", "-q", other).Run(); err != nil {
		t.Fatal("the repo's own *.log rule no longer applies outside .truehand/tasks")
	}
}
