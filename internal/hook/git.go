package hook

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spaghetticoders/truehand/internal/core"
)

const gitHookMarker = "# installed by truehand"

// Without the binary the hook warns instead of blocking every commit; CI still checks.
const requireBinary = "command -v truehand >/dev/null 2>&1 || { echo \"truehand not found: skipping the local check (CI still runs it)\" >&2; exit 0; }\n"

// GitHooks are for humans and agents without Claude Code integration: they enforce the same
// commit rules at the git level.
var GitHooks = map[string]string{
	"commit-msg": "#!/bin/sh\n" + gitHookMarker + "\n" + requireBinary + "exec truehand hook commit-msg \"$1\"\n",
	"pre-push":   "#!/bin/sh\n" + gitHookMarker + "\n" + requireBinary + "exec truehand hook pre-push\n",
}

// InstallGitHooks writes the hooks into the repo's hooks dir, refusing to overwrite foreign ones.
func InstallGitHooks(ws *core.Workspace, force bool) ([]string, error) {
	out, err := ws.Repo.Git("rev-parse", "--git-path", "hooks")
	if err != nil {
		return nil, err
	}
	dir := strings.TrimSpace(out)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(ws.Repo.Root, dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var installed []string
	for name, body := range GitHooks {
		p := filepath.Join(dir, name)
		if b, err := os.ReadFile(p); err == nil && !strings.Contains(string(b), gitHookMarker) && !force {
			return installed, fmt.Errorf("%s already exists and was not installed by truehand (use --force to replace it)", p)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return installed, err
		}
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			return installed, err
		}
		installed = append(installed, p)
	}
	return installed, nil
}
