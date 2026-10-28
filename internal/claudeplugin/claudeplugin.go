// Package claudeplugin installs Truehand's Claude Code plugin from the copy embedded in the
// binary, through the official `claude plugin` CLI.
package claudeplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spaghetticoders/truehand"
)

// PluginID is the plugin as Claude Code names it: plugin@marketplace.
const PluginID = "truehand@truehand"

// Dir is where the marketplace is written: ~/.truehand/claude-marketplace.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".truehand", "claude-marketplace"), nil
}

// Extract writes the embedded marketplace to dir, replacing any older copy, and places the
// running binary next to the plugin's shim so hooks work even when truehand is not on PATH.
func Extract(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	self, err := selfBinary()
	if err != nil {
		return err
	}
	return write(dir, self)
}

func selfBinary() ([]byte, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return os.ReadFile(self)
}

func write(dir string, self []byte) error {
	err := fs.WalkDir(truehand.ClaudePlugin, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := truehand.ClaudePlugin.ReadFile(p)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.HasPrefix(p, "plugin/bin/") {
			mode = 0o755
		}
		return os.WriteFile(dst, b, mode)
	})
	if err != nil {
		return err
	}
	name := fmt.Sprintf("truehand-%s-%s", unameS(), unameM())
	return os.WriteFile(filepath.Join(dir, "plugin", "bin", name), self, 0o755)
}

// The shim looks for truehand-$(uname -s)-$(uname -m).
func unameS() string {
	switch runtime.GOOS {
	case "darwin":
		return "Darwin"
	case "linux":
		return "Linux"
	}
	return runtime.GOOS
}

func unameM() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "arm64":
		if runtime.GOOS == "darwin" {
			return "arm64"
		}
		return "aarch64"
	}
	return runtime.GOARCH
}

// Claude runs the claude CLI; tests replace it.
var Claude = func(ctx context.Context, args ...string) ([]byte, error) {
	bin, err := exec.LookPath("claude")
	if err != nil {
		return nil, errors.New("Claude Code is not installed (no `claude` on PATH)")
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("claude %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Install writes the marketplace and installs (or refreshes) the plugin for the user.
func Install(ctx context.Context) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	if err := Extract(dir); err != nil {
		return "", err
	}
	if installed, _ := Status(ctx); installed {
		// Already installed: refresh the marketplace so Claude Code picks up the new copy.
		if _, err := Claude(ctx, "plugin", "marketplace", "update", "truehand"); err != nil {
			return dir, err
		}
	}
	if _, err := Claude(ctx, "plugin", "install", "truehand", "--marketplace", dir, "--scope", "user", "--json"); err != nil {
		return dir, err
	}
	return dir, nil
}

// Status reports whether the plugin is installed and enabled for the user.
func Status(ctx context.Context) (bool, error) {
	out, err := Claude(ctx, "plugin", "list", "--json")
	if err != nil {
		return false, err
	}
	var list []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Enabled *bool  `json:"enabled"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return false, fmt.Errorf("claude plugin list: unexpected output: %w", err)
	}
	for _, p := range list {
		if p.ID == PluginID || p.Name == PluginID {
			return p.Enabled == nil || *p.Enabled, nil
		}
	}
	return false, nil
}
