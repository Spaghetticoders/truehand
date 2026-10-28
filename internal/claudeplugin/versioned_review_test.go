package claudeplugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewStatusSettingsNeverDecides(t *testing.T) {
	b, err := StatusSettings("/Users/o'neil/My Apps/truehand")
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Async   bool   `json:"async"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"PreToolUse", "PermissionRequest", "SubagentStop", "PreCompact"} {
		if _, ok := s.Hooks[banned]; ok {
			t.Errorf("%s hook present", banned)
		}
	}
	for ev, ms := range s.Hooks {
		for _, m := range ms {
			for _, h := range m.Hooks {
				if !h.Async || h.Type != "command" {
					t.Errorf("%s: %+v", ev, h)
				}
				if h.Command != `'/Users/o'\''neil/My Apps/truehand' hook status` {
					t.Errorf("%s: command %q", ev, h.Command)
				}
			}
		}
	}
	if len(s.Hooks) == 0 {
		t.Fatal("no hooks")
	}
}

func TestReviewPruneKeepsCurrentInUseAndTemp(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"cur", "used", "old", ".tmp-x-1"} {
		os.MkdirAll(filepath.Join(root, d, "plugin"), 0o755)
	}
	removed, err := Prune(root, PluginDir(filepath.Join(root, "cur")), map[string]bool{PluginDir(filepath.Join(root, "used")): true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(removed, ",") != "old" {
		t.Fatalf("removed %v", removed)
	}
	for _, d := range []string{"cur", "used", ".tmp-x-1"} {
		if _, err := os.Stat(filepath.Join(root, d)); err != nil {
			t.Errorf("%s gone", d)
		}
	}
}

func TestReviewExtractVersionedIsIdempotentAndComplete(t *testing.T) {
	root := t.TempDir()
	a, err := extractVersioned(root, []byte("bin-v1"))
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(filepath.Dir(a), complete)
	fi1, err := os.Stat(marker)
	if err != nil {
		t.Fatal(err)
	}
	b, err := extractVersioned(root, []byte("bin-v1"))
	if err != nil || a != b {
		t.Fatalf("%s %s %v", a, b, err)
	}
	fi2, _ := os.Stat(marker)
	if !fi1.ModTime().Equal(fi2.ModTime()) {
		t.Fatal("a complete version was rewritten")
	}
	c, err := extractVersioned(root, []byte("bin-v2"))
	if err != nil || c == a {
		t.Fatalf("new binary, same dir: %s", c)
	}
	left, _ := filepath.Glob(filepath.Join(root, ".tmp-*"))
	if len(left) != 0 {
		t.Fatalf("temp dirs left: %v", left)
	}
}
