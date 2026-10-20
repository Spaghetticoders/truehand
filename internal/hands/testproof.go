package hands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/evidence"
	"github.com/spaghetticoders/truehand/internal/glob"
)

type TestProofOptions struct {
	Command string   // overrides commands.test_proof / commands.test
	Only    []string // restrict reverted files to these globs
	Timeout time.Duration
	Tee     io.Writer
}

type restoreEntry struct {
	Path    string      `json:"path"`
	Existed bool        `json:"existed"`
	Mode    fs.FileMode `json:"mode"`
	Backup  string      `json:"backup,omitempty"`
}

type restoreManifest struct {
	Task    string         `json:"task"`
	Created time.Time      `json:"created"`
	Entries []restoreEntry `json:"entries"`
}

func restoreDir(ws *core.Workspace) (string, error) {
	gd, err := ws.Repo.GitDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(gd, "truehand", "restore"), nil
}

// PendingRestore reports whether a previous test-proof run was interrupted before restoring.
func PendingRestore(ws *core.Workspace) (bool, error) {
	dir, err := restoreDir(ws)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(filepath.Join(dir, "manifest.json"))
	return err == nil, nil
}

// Restore puts back the files a test-proof run reverted. It is idempotent.
func Restore(ws *core.Workspace) (int, error) {
	dir, err := restoreDir(ws)
	if err != nil {
		return 0, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var m restoreManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return 0, fmt.Errorf("corrupt restore manifest %s: %w", dir, err)
	}
	for _, e := range m.Entries {
		dst := filepath.Join(ws.Repo.Root, filepath.FromSlash(e.Path))
		if !e.Existed {
			if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
				return 0, err
			}
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Backup))
		if err != nil {
			return 0, fmt.Errorf("backup of %s missing: %w", e.Path, err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return 0, err
		}
		if err := os.WriteFile(dst, data, e.Mode.Perm()); err != nil {
			return 0, err
		}
		_ = os.Chmod(dst, e.Mode.Perm())
	}
	return len(m.Entries), os.RemoveAll(dir)
}

// TestProof proves the task's tests catch the change: they pass with it and fail when the
// non-test part of the change is reverted to the task base.
func TestProof(ctx context.Context, ws *core.Workspace, t *board.Task, o TestProofOptions) (*Result, error) {
	if pending, err := PendingRestore(ws); err != nil {
		return nil, err
	} else if pending {
		return nil, errors.New("a previous test-proof run was interrupted: run `truehand hand test-proof --restore` first")
	}
	configured := ws.Config.TestProofCommand()
	if o.Command != "" && configured != "" && o.Command != configured {
		return nil, fmt.Errorf("the test command is configured as %q; test-proof runs exactly that", configured)
	}
	cmdline := o.Command
	if cmdline == "" {
		cmdline = configured
	}
	if cmdline == "" {
		return nil, errors.New("no test command: set commands.test in .truehand/config.yaml or pass --command")
	}
	s, err := Begin(ws, t, "test-proof", "")
	if err != nil {
		return nil, err
	}
	var fix, tests []string
	for p := range s.Rec.Inputs.Files {
		switch {
		case glob.MatchAny(ws.Config.TestGlobs(), p):
			tests = append(tests, p)
		case len(o.Only) == 0 || glob.MatchAny(o.Only, p):
			fix = append(fix, p)
		}
	}
	sortStrings(fix)
	sortStrings(tests)
	s.Rec.Data = map[string]any{"command": cmdline, "reverted": fix, "tests": tests, "only": o.Only}
	if len(fix) == 0 {
		return s.Finish(evidence.Error, "nothing to prove: the task changes no non-test files (check tests.globs or --only)", "")
	}

	withFix, err := RunShell(ctx, ws.Repo.Root, cmdline, o.Timeout, o.Tee)
	if err != nil {
		return nil, err
	}
	if err := s.WriteFile("with-change.log", logBytes(cmdline, withFix)); err != nil {
		return nil, err
	}
	if withFix.TimedOut || withFix.ExitCode != 0 {
		return s.Finish(evidence.Fail, fmt.Sprintf("tests fail even with the change (exit %d):\n%s", withFix.ExitCode, tail(withFix.Output, 15)), "make the tests pass first")
	}

	without, revertErr := runReverted(ctx, ws, s.Base, t.ID, fix, cmdline, o)
	if revertErr != nil {
		return nil, revertErr
	}
	if err := s.WriteFile("without-change.log", logBytes(cmdline, without)); err != nil {
		return nil, err
	}
	if err := s.WriteFile("reverted.txt", []byte(strings.Join(fix, "\n")+"\n")); err != nil {
		return nil, err
	}
	s.Rec.Data["exit_with"] = withFix.ExitCode
	s.Rec.Data["exit_without"] = without.ExitCode
	switch {
	case without.TimedOut:
		return s.Finish(evidence.Error, "tests timed out with the change reverted; cannot tell whether they catch it", "")
	case without.ExitCode == 0:
		return s.Finish(evidence.Fail, fmt.Sprintf("tests still pass with the change reverted (%d file(s)): they do not catch it", len(fix)), "write a test that fails without the change")
	case BuildFailure(without.Output) != "" && BuildFailure(withFix.Output) == "" && !ws.Config.Tests.AcceptBuildFailure:
		// A test that only fails because the reverted code no longer compiles proves that it
		// references new code, not that it checks behaviour: an assertion-free test passes.
		s.Rec.Data["build_failure"] = BuildFailure(without.Output)
		return s.Finish(evidence.Fail, fmt.Sprintf("without the change the tests do not compile (%s), so the proof cannot tell whether they check behaviour", BuildFailure(without.Output)),
			"narrow commands.test_proof to tests that compile on the base (keep new APIs as stubs on the base, or use --only to revert just the implementation); or set tests.accept_build_failure: true for this repo, knowingly")
	default:
		return s.Finish(evidence.Pass, fmt.Sprintf("tests pass with the change and fail without it (exit %d, %d file(s) reverted)", without.ExitCode, len(fix)), "")
	}
}

// runReverted reverts files to the task base, runs the tests and always restores, also on
// SIGINT/SIGTERM. The manifest is written before anything is touched.
func runReverted(ctx context.Context, ws *core.Workspace, base, taskID string, files []string, cmdline string, o TestProofOptions) (CmdResult, error) {
	dir, err := restoreDir(ws)
	if err != nil {
		return CmdResult{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return CmdResult{}, err
	}
	m := restoreManifest{Task: taskID, Created: time.Now().UTC()}
	for i, p := range files {
		abs := filepath.Join(ws.Repo.Root, filepath.FromSlash(p))
		e := restoreEntry{Path: p}
		if fi, err := os.Lstat(abs); err == nil {
			data, err := os.ReadFile(abs)
			if err != nil {
				return CmdResult{}, err
			}
			e.Existed, e.Mode, e.Backup = true, fi.Mode(), fmt.Sprintf("%d.bak", i)
			if err := os.WriteFile(filepath.Join(dir, e.Backup), data, 0o600); err != nil {
				return CmdResult{}, err
			}
		}
		m.Entries = append(m.Entries, e)
	}
	mb, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), mb, 0o600); err != nil {
		return CmdResult{}, err
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	runCtx, cancel := context.WithCancel(ctx)
	go func() {
		if _, ok := <-sig; ok {
			cancel()
		}
	}()
	defer func() {
		signal.Stop(sig)
		close(sig)
		cancel()
	}()

	restore := func() error {
		_, err := Restore(ws)
		return err
	}
	for _, p := range files {
		abs := filepath.Join(ws.Repo.Root, filepath.FromSlash(p))
		data, existed, err := ws.Repo.BlobAt(base, p)
		if err != nil {
			return CmdResult{}, errors.Join(err, restore())
		}
		if !existed {
			if err := os.Remove(abs); err != nil && !errors.Is(err, os.ErrNotExist) {
				return CmdResult{}, errors.Join(err, restore())
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return CmdResult{}, errors.Join(err, restore())
		}
		if err := os.WriteFile(abs, data, 0o644); err != nil {
			return CmdResult{}, errors.Join(err, restore())
		}
	}
	res, runErr := RunShell(runCtx, ws.Repo.Root, cmdline, o.Timeout, o.Tee)
	if err := restore(); err != nil {
		return res, fmt.Errorf("RESTORE FAILED, run `truehand hand test-proof --restore`: %w", err)
	}
	if runCtx.Err() == context.Canceled {
		return res, errors.New("interrupted; files restored")
	}
	return res, runErr
}

func logBytes(cmdline string, r CmdResult) []byte {
	return append([]byte(fmt.Sprintf("$ %s\n# exit %d in %s\n\n", cmdline, r.ExitCode, r.Duration.Round(time.Millisecond))), r.Output...)
}

var buildFailureRes = []struct {
	name string
	re   *regexp.Regexp
}{
	{"go build failed", regexp.MustCompile(`\[(build|setup) failed\]`)},
	{"dart compilation failed", regexp.MustCompile(`Compilation failed|Failed to load "[^"]+":`)},
	{"typescript error", regexp.MustCompile(`error TS\d+:`)},
	{"rust compile error", regexp.MustCompile(`error\[E\d+\]|could not compile`)},
	{"python import error", regexp.MustCompile(`errors? during collection|ModuleNotFoundError|ImportError: cannot import`)},
}

// BuildFailure names the kind of compile/load failure in a test run's output, or "".
func BuildFailure(out []byte) string {
	for _, b := range buildFailureRes {
		if b.re.Match(out) {
			return b.name
		}
	}
	return ""
}
