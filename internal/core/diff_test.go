package core

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDiff(t *testing.T) {
	dir, ws := repo(t)
	write(t, filepath.Join(dir, "lib/keep.go"), "package lib\n\nvar A = 1\nvar B = 2\n")
	write(t, filepath.Join(dir, "lib/gone.go"), "package lib\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "feat: base")
	task, err := ws.NewTask("T-1", "Demo", "", "lib/**")
	if err != nil {
		t.Fatal(err)
	}

	write(t, filepath.Join(dir, "lib/keep.go"), "package lib\n\nvar A = 10\n")
	git(t, dir, "rm", "-q", "lib/gone.go")
	write(t, filepath.Join(dir, "lib/committed.go"), "package lib\n")
	git(t, dir, "add", "lib/committed.go")
	git(t, dir, "commit", "-q", "-m", "feat: work")
	write(t, filepath.Join(dir, "lib/new.go"), "package lib\n\nvar N = 1\n")
	write(t, filepath.Join(dir, "README.md"), "outside scope\n")
	write(t, filepath.Join(dir, "lib/logo.bin"), "\x00\x01\x02")
	ws.Comment("T-1", "Truehand's own state must not show")

	d, err := ws.Diff(task, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []DiffFile{
		{Path: "lib/committed.go", Status: "A", Add: 1, InScope: true},
		{Path: "lib/gone.go", Status: "D", Del: 1, InScope: true},
		{Path: "lib/keep.go", Status: "M", Add: 1, Del: 2, InScope: true},
		{Path: "README.md", Status: "A", Add: 1},
		{Path: "lib/logo.bin", Status: "A", Binary: true, InScope: true},
		{Path: "lib/new.go", Status: "A", Add: 3, InScope: true},
	}
	if !reflect.DeepEqual(d.Files, want) {
		t.Fatalf("files\n got %+v\nwant %+v", d.Files, want)
	}
	if d.Base != task.Base || d.Head != git(t, dir, "rev-parse", "HEAD") || d.Truncated {
		t.Fatalf("base %s head %s truncated %v", d.Base, d.Head, d.Truncated)
	}
	for _, s := range []string{"+var A = 10", "-var B = 2", "deleted file mode", "+++ b/lib/new.go", "+outside scope", "Binary files"} {
		if !strings.Contains(d.Patch, s) {
			t.Fatalf("patch lacks %q:\n%s", s, d.Patch)
		}
	}
	if strings.Contains(d.Patch, ".truehand") {
		t.Fatalf("patch shows .truehand:\n%s", d.Patch)
	}

	one, err := ws.Diff(task, "lib/keep.go", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(one.Patch, "+var A = 10") || strings.Contains(one.Patch, "new.go") || len(one.Files) != len(want) {
		t.Fatalf("single-file patch:\n%s", one.Patch)
	}
	if one, _ = ws.Diff(task, "lib/new.go", 0); !strings.Contains(one.Patch, "+var N = 1") || strings.Contains(one.Patch, "keep.go") {
		t.Fatalf("untracked single-file patch:\n%s", one.Patch)
	}

	cut, err := ws.Diff(task, "", 120)
	if err != nil {
		t.Fatal(err)
	}
	if !cut.Truncated || len(cut.Patch) > 120 || !strings.HasSuffix(cut.Patch, "\n") || !strings.HasPrefix(d.Patch, cut.Patch) {
		t.Fatalf("truncated %v %q", cut.Truncated, cut.Patch)
	}

	only, err := ws.Diff(task, "", -1)
	if err != nil || only.Patch != "" || only.Truncated || !reflect.DeepEqual(only.Files, want) {
		t.Fatalf("files only: %+v %v", only, err)
	}

	for _, f := range []string{"go.mod", ".truehand/config.yaml", "../etc/passwd", ":(exclude)x"} {
		if _, err := ws.Diff(task, f, 0); err == nil {
			t.Fatalf("diff of unchanged file %q accepted", f)
		}
	}
}

func TestDiffWithoutScope(t *testing.T) {
	dir, ws := repo(t)
	task, err := ws.NewTask("T-1", "Demo", "")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "anything.txt"), "x\n")
	d, err := ws.Diff(task, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Files) != 1 || !d.Files[0].InScope {
		t.Fatalf("%+v", d.Files)
	}
}

func TestDiffFileIsLiteral(t *testing.T) {
	dir, ws := repo(t)
	task, err := ws.NewTask("T-1", "Demo", "")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "a.go"), "package a\n")
	write(t, filepath.Join(dir, "*.go"), "package star\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "feat: x")
	d, err := ws.Diff(task, "*.go", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.Patch, "+package star") || strings.Contains(d.Patch, "a.go") {
		t.Fatalf("file name taken as a pattern:\n%s", d.Patch)
	}
}
