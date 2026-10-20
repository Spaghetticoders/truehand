package claudeplugin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractWritesAMarketplaceWithTheBinaryNextToTheShim(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mkt")
	os.MkdirAll(filepath.Join(dir, "stale"), 0o755)
	if err := Extract(dir); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		".claude-plugin/marketplace.json",
		"plugin/.claude-plugin/plugin.json",
		"plugin/hooks/hooks.json",
		"plugin/.mcp.json",
		"plugin/skills/truehand/SKILL.md",
		"plugin/bin/truehand",
	} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "stale")); err == nil {
		t.Error("an older copy must be replaced")
	}
	bins, _ := filepath.Glob(filepath.Join(dir, "plugin/bin/truehand-*-*"))
	if len(bins) != 1 {
		t.Fatalf("expected the bundled binary next to the shim, got %v", bins)
	}
	if fi, _ := os.Stat(bins[0]); fi.Mode()&0o111 == 0 {
		t.Error("bundled binary must be executable")
	}
	if fi, _ := os.Stat(filepath.Join(dir, "plugin/bin/truehand")); fi.Mode()&0o111 == 0 {
		t.Error("shim must be executable")
	}
}

func fakeClaude(t *testing.T, listJSON string) *[][]string {
	var calls [][]string
	old := Claude
	Claude = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args)
		if len(args) >= 2 && args[1] == "list" {
			return []byte(listJSON), nil
		}
		return []byte("{}"), nil
	}
	t.Cleanup(func() { Claude = old })
	return &calls
}

func TestInstallFreshAndRefresh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	calls := fakeClaude(t, `[{"id":"other@x","enabled":true}]`)
	dir, err := Install(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	last := strings.Join((*calls)[len(*calls)-1], " ")
	if last != "plugin install truehand --marketplace "+dir+" --scope user --json" {
		t.Fatalf("unexpected install call: %q", last)
	}
	for _, c := range *calls {
		if c[1] == "marketplace" {
			t.Fatal("a fresh install must not refresh a marketplace that does not exist yet")
		}
	}

	calls = fakeClaude(t, `[{"id":"truehand@truehand","enabled":true}]`)
	if _, err := Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	refreshed := false
	for _, c := range *calls {
		if strings.Join(c, " ") == "plugin marketplace update truehand" {
			refreshed = true
		}
	}
	if !refreshed {
		t.Fatalf("reinstall must refresh the marketplace: %v", *calls)
	}
}

func TestStatus(t *testing.T) {
	for list, want := range map[string]bool{
		`[{"id":"truehand@truehand","enabled":true}]`:     true,
		`[{"id":"truehand@truehand","enabled":false}]`:    false,
		`[{"id":"claude-mem@thedotmack","enabled":true}]`: false,
		`[]`: false,
	} {
		fakeClaude(t, list)
		got, err := Status(context.Background())
		if err != nil || got != want {
			t.Errorf("Status(%s) = %v, %v; want %v", list, got, err, want)
		}
	}
}
