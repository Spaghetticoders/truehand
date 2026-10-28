package claudeplugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spaghetticoders/truehand"
)

// complete marks a version directory whose extraction finished.
const complete = ".complete"

// VersionsDir is ~/.truehand/plugin.
func VersionsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".truehand", "plugin"), nil
}

// Version names the embedded plugin together with the binary placed next to its shim.
func Version(self []byte) (string, error) {
	h := sha256.New()
	err := fs.WalkDir(truehand.ClaudePlugin, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := truehand.ClaudePlugin.ReadFile(p)
		if err != nil {
			return err
		}
		h.Write([]byte(p))
		h.Write([]byte{0})
		h.Write(b)
		h.Write([]byte{0})
		return nil
	})
	if err != nil {
		return "", err
	}
	h.Write(self)
	return hex.EncodeToString(h.Sum(nil))[:12], nil
}

// ExtractVersioned writes this binary's plugin to root/<version> and returns the plugin
// directory to pass to `claude --plugin-dir`. A version is written once, into a temporary
// directory renamed into place, and never touched again: sessions started from an older
// version keep their copy.
func ExtractVersioned(root string) (string, error) {
	self, err := selfBinary()
	if err != nil {
		return "", err
	}
	return extractVersioned(root, self)
}

func extractVersioned(root string, self []byte) (string, error) {
	v, err := Version(self)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, v)
	if _, err := os.Stat(filepath.Join(dir, complete)); err == nil {
		return PluginDir(dir), nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(root, ".tmp-"+v+"-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if err := write(tmp, self); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(tmp, complete), nil, 0o644); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dir); err != nil {
		// Another process finished the same version first, or an interrupted run left a
		// partial copy: only a complete one is used.
		if _, serr := os.Stat(filepath.Join(dir, complete)); serr == nil {
			return PluginDir(dir), nil
		}
		return "", err
	}
	return PluginDir(dir), nil
}

const staleTmp = time.Hour

// PluginDir is the plugin inside a version directory.
func PluginDir(versionDir string) string { return filepath.Join(versionDir, "plugin") }

// Prune removes the version directories under root that are neither current nor used by a
// session (inUse holds plugin directories, as returned by ExtractVersioned).
func Prune(root, current string, inUse map[string]bool) ([]string, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), ".tmp-") {
			// Left by an extraction that was killed; a live one finishes within seconds.
			if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > staleTmp {
				if err := os.RemoveAll(filepath.Join(root, e.Name())); err == nil {
					removed = append(removed, e.Name())
				}
			}
			continue
		}
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		pd := PluginDir(filepath.Join(root, e.Name()))
		if pd == current || inUse[pd] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
			return removed, err
		}
		removed = append(removed, e.Name())
	}
	return removed, nil
}

// StatusEvents are the hook events that report a session's state to the app. None of them
// can block or steer the agent, and the hooks run async. PostToolUse tells that a permission
// prompt was answered and the agent is working again.
var StatusEvents = []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "Notification", "Stop", "SessionEnd"}

// StatusSettings is the settings file passed with `claude --settings`: async status hooks
// that run `<bin> hook status`, which prints nothing and always exits 0.
func StatusSettings(bin string) ([]byte, error) {
	if bin == "" {
		return nil, errors.New("no binary for the status hook")
	}
	type hookCmd struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Async   bool   `json:"async"`
		Timeout int    `json:"timeout"`
	}
	type matcher struct {
		Hooks []hookCmd `json:"hooks"`
	}
	hooks := map[string][]matcher{}
	for _, ev := range StatusEvents {
		hooks[ev] = []matcher{{Hooks: []hookCmd{{Type: "command", Command: shellQuote(bin) + " hook status", Async: true, Timeout: 5}}}}
	}
	b, err := json.MarshalIndent(map[string]any{"hooks": hooks}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
