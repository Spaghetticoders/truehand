package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spaghetticoders/truehand/internal/config"
	"github.com/spaghetticoders/truehand/internal/gate"
	"github.com/spaghetticoders/truehand/internal/gitx"
)

const GitHookMarker = "# installed by truehand"

// Without the binary the hook warns instead of blocking every commit; CI still checks.
const requireBinary = "command -v truehand >/dev/null 2>&1 || { echo \"truehand not found: skipping the local check (CI still runs it)\" >&2; exit 0; }\n"

// GitHooks are for humans and agents without Claude Code integration: they enforce the same
// commit rules at the git level.
var GitHooks = map[string]string{
	"commit-msg": "#!/bin/sh\n" + GitHookMarker + "\n" + requireBinary + "exec truehand hook commit-msg \"$1\"\n",
	"pre-push":   "#!/bin/sh\n" + GitHookMarker + "\n" + requireBinary + "exec truehand hook pre-push\n",
}

// Git hook states as an activation reports them.
const (
	HookOurs    = "ours"
	HookForeign = "foreign"
	HookMissing = "missing"
)

func hooksDir(repo *gitx.Repo) (string, error) {
	out, err := repo.Git("rev-parse", "--git-path", "hooks")
	if err != nil {
		return "", err
	}
	dir := strings.TrimSpace(out)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(repo.Root, dir)
	}
	return dir, nil
}

func hookNames() []string {
	names := make([]string, 0, len(GitHooks))
	for n := range GitHooks {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// InstallGitHooks writes the hooks into the repo's hooks dir, refusing to overwrite foreign ones.
func InstallGitHooks(repo *gitx.Repo, force bool) ([]string, error) {
	dir, err := hooksDir(repo)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var installed []string
	for _, name := range hookNames() {
		p := filepath.Join(dir, name)
		if b, err := os.ReadFile(p); err == nil && !strings.Contains(string(b), GitHookMarker) && !force {
			return installed, fmt.Errorf("%s already exists and was not installed by truehand (use --force to replace it)", p)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return installed, err
		}
		if err := os.WriteFile(p, []byte(GitHooks[name]), 0o755); err != nil {
			return installed, err
		}
		installed = append(installed, p)
	}
	return installed, nil
}

// GitHookStates reports, per Truehand hook, whether the repo has ours, someone else's or none.
func GitHookStates(repo *gitx.Repo) (map[string]string, error) {
	dir, err := hooksDir(repo)
	if err != nil {
		return nil, err
	}
	states := map[string]string{}
	for name := range GitHooks {
		b, err := os.ReadFile(filepath.Join(dir, name))
		switch {
		case errors.Is(err, os.ErrNotExist):
			states[name] = HookMissing
		case err != nil:
			return nil, err
		case strings.Contains(string(b), GitHookMarker):
			states[name] = HookOurs
		default:
			states[name] = HookForeign
		}
	}
	return states, nil
}

// DetectBase picks the branch tasks are measured against: the remote default branch when
// there is one, else the current local branch.
func DetectBase(repo *gitx.Repo) string {
	if out, err := repo.Git("symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		return strings.TrimSpace(out)
	}
	for _, ref := range []string{"origin/main", "origin/master"} {
		if _, err := repo.Git("rev-parse", "--verify", "-q", ref); err == nil {
			return ref
		}
	}
	if out, err := repo.Git("branch", "--show-current"); err == nil && strings.TrimSpace(out) != "" {
		return strings.TrimSpace(out)
	}
	return "main"
}

func DetectStack(root string) string {
	for file, stack := range map[string]string{"pubspec.yaml": "flutter", "go.mod": "go", "package.json": "node"} {
		if _, err := os.Stat(filepath.Join(root, file)); err == nil {
			return stack
		}
	}
	return "generic"
}

// InitConfig writes .truehand/config.yaml from the stack's template; empty stack and base are
// detected. It returns the stack used.
func InitConfig(repo *gitx.Repo, stack, base string, force bool) (string, error) {
	if stack == "" {
		stack = DetectStack(repo.Root)
	}
	if base == "" {
		base = DetectBase(repo)
	} else if err := validBase(repo, base); err != nil {
		return "", err
	}
	tpl, err := config.Template(stack, base)
	if err != nil {
		return "", err
	}
	if err := ValidateConfig([]byte(tpl)); err != nil {
		return "", err
	}
	if cfg, err := config.Parse([]byte(tpl)); err != nil || cfg.Base != base {
		return "", fmt.Errorf("invalid base %q", base)
	}
	p := config.Path(repo.Root)
	if _, err := os.Stat(p); err == nil && !force {
		return "", fmt.Errorf("%s already exists (use --force to overwrite)", p)
	}
	if err := os.MkdirAll(filepath.Join(repo.Root, config.Dir, "tasks"), 0o755); err != nil {
		return "", err
	}
	return stack, os.WriteFile(p, []byte(tpl), 0o644)
}

// validBase accepts a branch name such as main or origin/main; it is written into YAML.
func validBase(repo *gitx.Repo, base string) error {
	if strings.HasPrefix(base, "-") {
		return fmt.Errorf("invalid base %q", base)
	}
	if _, err := repo.Git("check-ref-format", "--allow-onelevel", "refs/heads/"+base); err != nil {
		return fmt.Errorf("invalid base %q: not a branch name", base)
	}
	return nil
}

// ValidateConfig checks a config.yaml as Open would, so a broken one is never written.
func ValidateConfig(yaml []byte) error {
	cfg, err := config.Parse(yaml)
	if err != nil {
		return err
	}
	return gate.Validate(cfg)
}

// WriteConfig replaces .truehand/config.yaml after validating it.
func WriteConfig(repo *gitx.Repo, yaml []byte) error {
	if err := ValidateConfig(yaml); err != nil {
		return err
	}
	p := config.Path(repo.Root)
	if err := os.MkdirAll(filepath.Join(repo.Root, config.Dir, "tasks"), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, yaml, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Activation is whether a repo is set up for Truehand, and what activating it would use.
type Activation struct {
	HasConfig   bool   `json:"has_config"`
	ConfigYAML  string `json:"config_yaml"`
	ConfigError string `json:"config_error,omitempty"`
	Stack       string `json:"stack"`
	Base        string `json:"base"`
	// GitHooks maps each hook to HookOurs, HookForeign or HookMissing.
	GitHooks map[string]string `json:"git_hooks"`
	// ConfigCommitted is true when config.yaml is tracked and unchanged: only then do task
	// worktrees, made from the base, get it.
	ConfigCommitted bool `json:"config_committed"`
}

func ActivationState(repo *gitx.Repo) (*Activation, error) {
	a := &Activation{Stack: DetectStack(repo.Root), Base: DetectBase(repo)}
	b, err := os.ReadFile(config.Path(repo.Root))
	switch {
	case err == nil:
		a.HasConfig = true
		a.ConfigYAML = string(b)
		if err := ValidateConfig(b); err != nil {
			a.ConfigError = err.Error()
		} else if cfg, _ := config.Parse(b); cfg.Base != "" {
			a.Base = cfg.Base
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	if a.GitHooks, err = GitHookStates(repo); err != nil {
		return nil, err
	}
	a.ConfigCommitted = a.HasConfig && configCommitted(repo)
	return a, nil
}

func configCommitted(repo *gitx.Repo) bool {
	rel := filepath.ToSlash(filepath.Join(config.Dir, config.File))
	if _, err := repo.Git("ls-files", "--error-unmatch", "--", rel); err != nil {
		return false
	}
	out, err := repo.Git("status", "--porcelain", "--", rel)
	return err == nil && strings.TrimSpace(out) == ""
}

// ActivateOptions choose the stack and base of a new config (empty: detected) and whether
// foreign git hooks may be replaced.
type ActivateOptions struct {
	Stack      string
	Base       string
	ForceHooks bool
}

// Activate writes the config when there is none (an existing one is kept: edit it with
// WriteConfig) and installs the git hooks.
func Activate(repo *gitx.Repo, opt ActivateOptions) (*Activation, error) {
	if _, err := os.Stat(config.Path(repo.Root)); errors.Is(err, os.ErrNotExist) {
		if _, err := InitConfig(repo, opt.Stack, opt.Base, false); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if _, err := InstallGitHooks(repo, opt.ForceHooks); err != nil {
		return nil, err
	}
	return ActivationState(repo)
}
