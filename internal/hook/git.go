package hook

import (
	"github.com/spaghetticoders/truehand/internal/core"
)

// GitHooks are for humans and agents without Claude Code integration: they enforce the same
// commit rules at the git level.
var GitHooks = core.GitHooks

// InstallGitHooks writes the hooks into the repo's hooks dir, refusing to overwrite foreign ones.
func InstallGitHooks(ws *core.Workspace, force bool) ([]string, error) {
	return core.InstallGitHooks(ws.Repo, force)
}
