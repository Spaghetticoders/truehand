// Package gitx wraps the few git plumbing commands Truehand needs.
package gitx

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// EmptyTree is git's well-known hash of the empty tree, used as base in repos with no base ref.
const EmptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

type Repo struct {
	Root string
}

func Open(dir string) (*Repo, error) {
	out, err := run(dir, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("not a git repository: %s", dir)
	}
	return &Repo{Root: strings.TrimSpace(out)}, nil
}

func (r *Repo) Git(args ...string) (string, error) {
	return run(r.Root, nil, args...)
}

// GitInput is Git with stdin, for the batch commands.
func (r *Repo) GitInput(stdin []byte, args ...string) (string, error) {
	return run(r.Root, stdin, args...)
}

func run(dir string, stdin []byte, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// HasCommits reports whether HEAD points at a commit.
func (r *Repo) HasCommits() bool {
	_, err := r.Git("rev-parse", "--verify", "-q", "HEAD")
	return err == nil
}

// Base resolves the commit a new task's diff is computed against: the merge-base of HEAD
// and baseRef when it exists, HEAD itself otherwise, or the empty tree in a repo with no commits.
func (r *Repo) Base(baseRef string) string {
	if !r.HasCommits() {
		return EmptyTree
	}
	if baseRef != "" {
		if mb, err := r.Git("merge-base", "HEAD", baseRef); err == nil {
			mb = strings.TrimSpace(mb)
			if head, _ := r.Git("rev-parse", "HEAD"); strings.TrimSpace(head) != mb {
				return mb
			}
		}
	}
	head, _ := r.Git("rev-parse", "HEAD")
	return strings.TrimSpace(head)
}

// ChangedFiles lists files that differ between base and the working tree, including
// untracked files not ignored by .gitignore. Paths under any excluded prefix are dropped.
func (r *Repo) ChangedFiles(base string, exclude ...string) ([]string, error) {
	set := map[string]bool{}
	out, err := r.Git("diff", "--name-only", "--no-renames", "-z", base)
	if err != nil {
		return nil, err
	}
	for _, p := range splitZ(out) {
		set[p] = true
	}
	out, err = r.Git("ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	for _, p := range splitZ(out) {
		set[p] = true
	}
	var files []string
	for p := range set {
		if excluded(p, exclude) {
			continue
		}
		files = append(files, p)
	}
	sort.Strings(files)
	return files, nil
}

func excluded(p string, prefixes []string) bool {
	for _, pre := range prefixes {
		if p == pre || strings.HasPrefix(p, strings.TrimSuffix(pre, "/")+"/") {
			return true
		}
	}
	return false
}

// HashFiles returns git blob hashes for paths relative to the repo root, after git's clean
// filters (eol, LFS), so a laptop and a CI runner hash the same content identically.
// Missing files map to "deleted".
func (r *Repo) HashFiles(paths []string) (map[string]string, error) {
	res := make(map[string]string, len(paths))
	var present []string
	for _, p := range paths {
		if fileExists(r.Root, p) {
			present = append(present, p)
		} else {
			res[p] = "deleted"
		}
	}
	if len(present) == 0 {
		return res, nil
	}
	out, err := run(r.Root, []byte(strings.Join(present, "\n")+"\n"), "hash-object", "--stdin-paths")
	if err != nil {
		return nil, err
	}
	hashes := strings.Fields(out)
	if len(hashes) != len(present) {
		return nil, errors.New("git hash-object: unexpected output length")
	}
	for i, p := range present {
		res[p] = hashes[i]
	}
	return res, nil
}

func (r *Repo) ConfigValue(key string) string {
	out, _ := r.Git("config", "--get", key)
	return strings.TrimSpace(out)
}

// CommitSubjects returns subjects of commits in base..HEAD, oldest first.
func (r *Repo) CommitSubjects(base string) ([]string, error) {
	if !r.HasCommits() {
		return nil, nil
	}
	rangeSpec := "HEAD"
	if base != EmptyTree {
		rangeSpec = base + "..HEAD"
	}
	out, err := r.Git("log", "--reverse", "--format=%s", rangeSpec)
	if err != nil {
		return nil, err
	}
	var subjects []string
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if l != "" {
			subjects = append(subjects, l)
		}
	}
	return subjects, nil
}

func splitZ(s string) []string {
	var out []string
	for _, p := range strings.Split(s, "\x00") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// GitDir returns the absolute path of the repository's .git directory (per-clone state lives there).
func (r *Repo) GitDir() (string, error) {
	out, err := r.Git("rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// BlobAt returns the content of path at the given commit, and whether it exists there.
func (r *Repo) BlobAt(commit, path string) ([]byte, bool, error) {
	if commit == EmptyTree {
		return nil, false, nil
	}
	if _, err := r.Git("cat-file", "-e", commit+":"+path); err != nil {
		return nil, false, nil
	}
	out, err := r.Git("cat-file", "blob", commit+":"+path)
	if err != nil {
		return nil, false, err
	}
	return []byte(out), true, nil
}

// Dirty returns which of paths differ from HEAD in the working tree or index, or are untracked.
func (r *Repo) Dirty(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	args := append([]string{"status", "--porcelain", "-z", "--untracked-files=all", "--"}, paths...)
	out, err := r.Git(args...)
	if err != nil {
		return nil, err
	}
	var dirty []string
	for _, e := range splitZ(out) {
		if len(e) > 3 {
			dirty = append(dirty, e[3:])
		}
	}
	return dirty, nil
}
