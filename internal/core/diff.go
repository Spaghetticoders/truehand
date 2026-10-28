package core

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/evidence"
	"github.com/spaghetticoders/truehand/internal/glob"
)

// DiffFile is one changed file. Status is git's letter: A, M, D, T; untracked files are A.
type DiffFile struct {
	Path    string `json:"path"`
	Status  string `json:"status"`
	Add     int    `json:"add"`
	Del     int    `json:"del"`
	Binary  bool   `json:"binary,omitempty"`
	InScope bool   `json:"in_scope"`
}

// Diff is a task's change against BaseOf(t): commits and the working tree, untracked files
// included, Truehand's own state excluded.
type Diff struct {
	Base      string     `json:"base"`
	Head      string     `json:"head"`
	Files     []DiffFile `json:"files"`
	Patch     string     `json:"patch"`
	Truncated bool       `json:"truncated"`
}

// Diff returns the task's changed files and the patch of one of them, or of all when file
// is empty, cut at limit bytes (0: no limit; negative: files only, no patch).
func (w *Workspace) Diff(t *board.Task, file string, limit int) (*Diff, error) {
	base, err := w.BaseOf(t)
	if err != nil {
		return nil, err
	}
	d := &Diff{Base: base, Files: []DiffFile{}}
	if head, err := w.Repo.Git("rev-parse", "-q", "--verify", "HEAD"); err == nil {
		d.Head = strings.TrimSpace(head)
	}
	pathspec := []string{"--", "."}
	for _, ex := range evidence.ExcludedPaths {
		pathspec = append(pathspec, ":(exclude)"+ex)
	}
	status, err := w.Repo.Git(append([]string{"diff", "--name-status", "--no-renames", "-z", base}, pathspec...)...)
	if err != nil {
		return nil, err
	}
	numstat, err := w.Repo.Git(append([]string{"diff", "--numstat", "--no-renames", "-z", base}, pathspec...)...)
	if err != nil {
		return nil, err
	}
	counts := parseNumstat(numstat)
	f := strings.Split(strings.TrimSuffix(status, "\x00"), "\x00")
	for i := 0; i+1 < len(f); i += 2 {
		df := DiffFile{Path: f[i+1], Status: f[i][:1]}
		c := counts[df.Path]
		df.Add, df.Del, df.Binary = c.add, c.del, c.binary
		d.Files = append(d.Files, df)
	}
	untracked, err := w.Repo.Git(append([]string{"ls-files", "-z", "--others", "--exclude-standard"}, pathspec...)...)
	if err != nil {
		return nil, err
	}
	isNew := map[string]bool{}
	for _, p := range strings.Split(strings.TrimSuffix(untracked, "\x00"), "\x00") {
		if p == "" {
			continue
		}
		isNew[p] = true
		c := countOf(strings.SplitN(w.noIndexDiff(p, "--numstat", "-z"), "\t", 3))
		d.Files = append(d.Files, DiffFile{Path: p, Status: "A", Add: c.add, Binary: c.binary})
	}
	found := file == ""
	for i := range d.Files {
		d.Files[i].InScope = len(t.Scope) == 0 || glob.MatchAny(t.Scope, d.Files[i].Path)
		found = found || d.Files[i].Path == file
	}
	if !found {
		return nil, fmt.Errorf("%s is not changed by task %s", file, t.ID)
	}
	if limit < 0 {
		return d, nil
	}
	var patch strings.Builder
	if file == "" || !isNew[file] {
		spec := pathspec
		if file != "" {
			spec = []string{"--", ":(literal)" + file}
		}
		p, err := w.Repo.Git(append([]string{"diff", "--no-renames", "--no-color", base}, spec...)...)
		if err != nil {
			return nil, err
		}
		patch.WriteString(p)
	}
	for _, df := range d.Files {
		if isNew[df.Path] && (file == "" || file == df.Path) {
			patch.WriteString(w.noIndexDiff(df.Path, "--no-color"))
		}
	}
	d.Patch, d.Truncated = cut(patch.String(), limit)
	return d, nil
}

// noIndexDiff diffs an untracked file against nothing. git exits 1 when there is a
// difference, so its output is taken whatever the exit code.
func (w *Workspace) noIndexDiff(path string, args ...string) string {
	cmd := exec.Command("git", append(append([]string{"diff", "--no-index"}, args...), "--", "/dev/null", path)...)
	cmd.Dir = w.Repo.Root
	var out bytes.Buffer
	cmd.Stdout = &out
	_ = cmd.Run()
	return out.String()
}

type lineCount struct {
	add, del int
	binary   bool
}

// parseNumstat reads `git diff --numstat -z --no-renames`: "add\tdel\tpath\0" per file.
func parseNumstat(out string) map[string]lineCount {
	res := map[string]lineCount{}
	for _, rec := range strings.Split(out, "\x00") {
		if parts := strings.SplitN(rec, "\t", 3); len(parts) == 3 {
			res[parts[2]] = countOf(parts)
		}
	}
	return res
}

// countOf reads the add and del columns of a numstat line; "-" marks a binary file.
func countOf(parts []string) lineCount {
	if len(parts) < 2 {
		return lineCount{}
	}
	if parts[0] == "-" {
		return lineCount{binary: true}
	}
	add, _ := strconv.Atoi(parts[0])
	del, _ := strconv.Atoi(parts[1])
	return lineCount{add: add, del: del}
}

// cut shortens s to at most limit bytes, ending on a whole line.
func cut(s string, limit int) (string, bool) {
	if limit <= 0 || len(s) <= limit {
		return s, false
	}
	s = s[:limit]
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[:i+1]
	}
	return s, true
}
