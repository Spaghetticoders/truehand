package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// On a case-insensitive file system (macOS by default) .TRUEHAND is .truehand.
func TestReviewCaseInsensitiveTruehand(t *testing.T) {
	ws := workspace(t, cfg)
	root := ws.Repo.Root
	if _, err := os.Stat(filepath.Join(root, ".TRUEHAND", "config.yaml")); err != nil {
		t.Skip("case-sensitive file system")
	}
	for _, c := range []string{
		"echo x > .TRUEHAND/config.yaml",
		"rm -rf .Truehand/tasks",
		"cp /tmp/x .TRUEHAND/config.yaml",
		"sed -i s/a/b/ .TrueHand/config.yaml",
	} {
		if d := CheckBash(ws, root, c); !d.Deny {
			t.Errorf("must deny %q", c)
		}
	}
	if d := CheckWrite(ws, filepath.Join(root, ".TRUEHAND/config.yaml")); !d.Deny {
		t.Error("Write to .TRUEHAND/config.yaml allowed")
	}
}

// The repo's own path written in another case reaches the same files.
func TestReviewCaseInsensitiveRepoRoot(t *testing.T) {
	ws := workspace(t, cfg)
	root := ws.Repo.Root
	upper := filepath.Join(filepath.Dir(root), strings.ToUpper(filepath.Base(root)))
	if _, err := os.Stat(filepath.Join(upper, ".truehand", "config.yaml")); err != nil {
		t.Skip("case-sensitive file system")
	}
	p := filepath.Join(upper, ".truehand", "config.yaml")
	if d := CheckWrite(ws, p); !d.Deny {
		t.Errorf("Write to %s allowed", p)
	}
	if d := CheckBash(ws, root, "echo x > "+p); !d.Deny {
		t.Errorf("echo into %s allowed", p)
	}
}
