package claudeplugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExtractVersionedNeverReplacesAVersionInUse(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugin")
	old, err := extractVersioned(root, []byte("binary v1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".claude-plugin/plugin.json", "hooks/hooks.json", "bin/truehand"} {
		if _, err := os.Stat(filepath.Join(old, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
	marker := filepath.Join(old, "session-was-here")
	os.WriteFile(marker, nil, 0o644)

	same, err := extractVersioned(root, []byte("binary v1"))
	if err != nil || same != old {
		t.Fatalf("same binary must reuse its version: %s %v", same, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("re-extracting the same version must not rewrite it")
	}

	cur, err := extractVersioned(root, []byte("binary v2"))
	if err != nil || cur == old {
		t.Fatalf("a new binary is a new version: %s %v", cur, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("extracting a new version must leave the old one alone")
	}
	bins, _ := filepath.Glob(filepath.Join(cur, "bin", "truehand-*-*"))
	if len(bins) != 1 {
		t.Fatalf("bundled binary: %v", bins)
	}
	if b, _ := os.ReadFile(bins[0]); string(b) != "binary v2" {
		t.Fatal("wrong bundled binary")
	}
	tmps, _ := filepath.Glob(filepath.Join(root, ".tmp-*"))
	if len(tmps) != 0 {
		t.Fatalf("temporary dirs left: %v", tmps)
	}

	removed, err := Prune(root, cur, map[string]bool{})
	if err != nil || len(removed) != 1 {
		t.Fatalf("prune: %v %v", removed, err)
	}
	if _, err := os.Stat(cur); err != nil {
		t.Fatal("prune removed the current version")
	}
}

func TestPruneKeepsVersionsUsedBySessions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugin")
	old, _ := extractVersioned(root, []byte("a"))
	cur, _ := extractVersioned(root, []byte("b"))
	removed, err := Prune(root, cur, map[string]bool{old: true})
	if err != nil || len(removed) != 0 {
		t.Fatalf("%v %v", removed, err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("a version used by a session was removed")
	}
}

func TestIncompleteVersionIsNotUsed(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugin")
	v, _ := Version([]byte("x"))
	os.MkdirAll(filepath.Join(root, v, "plugin"), 0o755) // interrupted extraction, no marker
	if _, err := extractVersioned(root, []byte("x")); err == nil {
		t.Fatal("a partial copy must not be handed out as complete")
	}
}

func TestStatusSettingsOnlyAsyncStatusHooks(t *testing.T) {
	b, err := StatusSettings("/Users/me/it's here/truehand")
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type, Command string
				Async         bool
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"SessionStart": true, "UserPromptSubmit": true, "PostToolUse": true, "Notification": true, "Stop": true, "SessionEnd": true}
	if len(s.Hooks) != len(allowed) {
		t.Fatalf("events: %v", s.Hooks)
	}
	for ev, ms := range s.Hooks {
		if !allowed[ev] {
			t.Errorf("unexpected hook event %s", ev)
		}
		for _, m := range ms {
			for _, h := range m.Hooks {
				if !h.Async || h.Type != "command" || h.Command != `'/Users/me/it'\''s here/truehand' hook status` {
					t.Errorf("%s: %+v", ev, h)
				}
			}
		}
	}
	if strings.Contains(string(b), "PreToolUse") || strings.Contains(string(b), "PermissionRequest") {
		t.Fatal("status settings must never include decision hooks")
	}
}

func TestPruneRemovesOnlyOldInterruptedExtractions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugin")
	cur, _ := extractVersioned(root, []byte("a"))
	old, fresh := filepath.Join(root, ".tmp-abc-1"), filepath.Join(root, ".tmp-abc-2")
	os.MkdirAll(old, 0o755)
	os.MkdirAll(fresh, 0o755)
	past := time.Now().Add(-2 * time.Hour)
	os.Chtimes(old, past, past)
	if _, err := Prune(root, cur, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("an interrupted extraction from hours ago must be removed")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("an extraction that may still be running must be left alone")
	}
}
