package core

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spaghetticoders/truehand/internal/config"
	"github.com/spaghetticoders/truehand/internal/gitx"
)

func TestActivate(t *testing.T) {
	dir := bareRepo(t)
	write(t, filepath.Join(dir, "go.mod"), "module x\n")
	r, err := gitx.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := ActivationState(r)
	if err != nil {
		t.Fatal(err)
	}
	missing := map[string]string{"commit-msg": HookMissing, "pre-push": HookMissing}
	if a.HasConfig || a.ConfigCommitted || a.Stack != "go" || a.Base != "main" || !reflect.DeepEqual(a.GitHooks, missing) {
		t.Fatalf("before: %+v", a)
	}

	foreign := filepath.Join(dir, ".git/hooks/pre-push")
	write(t, foreign, "#!/bin/sh\necho mine\n")
	if _, err := Activate(r, ActivateOptions{}); err == nil || !strings.Contains(err.Error(), "not installed by truehand") {
		t.Fatalf("foreign hook replaced without force: %v", err)
	}
	if b, _ := os.ReadFile(foreign); string(b) != "#!/bin/sh\necho mine\n" {
		t.Fatalf("foreign hook changed: %q", b)
	}
	if a, _ = ActivationState(r); a.GitHooks["pre-push"] != HookForeign || !a.HasConfig || a.ConfigError != "" {
		t.Fatalf("after refused activation: %+v", a)
	}

	a, err = Activate(r, ActivateOptions{Stack: "generic", ForceHooks: true})
	if err != nil {
		t.Fatal(err)
	}
	ours := map[string]string{"commit-msg": HookOurs, "pre-push": HookOurs}
	if !reflect.DeepEqual(a.GitHooks, ours) || !strings.Contains(a.ConfigYAML, `test: "go test ./..."`) {
		t.Fatalf("existing config overwritten or hooks not ours: %+v", a)
	}
	if _, err := Open(dir); err != nil {
		t.Fatalf("activated repo does not open: %v", err)
	}

	git(t, dir, "add", ".truehand/config.yaml")
	git(t, dir, "commit", "-q", "-m", "chore: truehand")
	if a, _ = ActivationState(r); !a.ConfigCommitted {
		t.Fatal("committed config not reported")
	}
	write(t, config.Path(dir), a.ConfigYAML+"\n# edited\n")
	if a, _ = ActivationState(r); a.ConfigCommitted {
		t.Fatal("modified config reported as committed")
	}
}

func TestActivationBaseFromConfig(t *testing.T) {
	dir, ws := repo(t)
	write(t, config.Path(dir), strings.Replace(testConfig, "base: main", "base: develop", 1))
	a, err := ActivationState(ws.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if a.Base != "develop" || a.Stack != "generic" {
		t.Fatalf("%+v", a)
	}
	write(t, config.Path(dir), "version: 0\ngates:\n  approved:\n    - check: nope\n")
	if a, _ = ActivationState(ws.Repo); a.ConfigError == "" || !strings.Contains(a.ConfigError, "unknown check") {
		t.Fatalf("invalid config not reported: %+v", a)
	}
}

func TestInitConfigRefusesExisting(t *testing.T) {
	dir, ws := repo(t)
	if _, err := InitConfig(ws.Repo, "", "", false); err == nil {
		t.Fatal("existing config overwritten")
	}
	if b, _ := os.ReadFile(config.Path(dir)); string(b) != testConfig {
		t.Fatalf("config changed: %q", b)
	}
	if _, err := InitConfig(ws.Repo, "cobol", "", true); err == nil {
		t.Fatal("unknown stack accepted")
	}
	stack, err := InitConfig(ws.Repo, "", "", true)
	if err != nil || stack != "generic" {
		t.Fatalf("stack %q err %v", stack, err)
	}
}

func TestWriteConfigValidates(t *testing.T) {
	dir, ws := repo(t)
	for _, bad := range []string{
		"version: 0\ngates:\n  approved:\n    - check: nope\n",
		"version: 0\ngate: {}\n",
		"version: 0\ngates:\n  shipped: []\n",
		"version: 0\ngates:\n  verified:\n    - check: hand_passed\n      with: { hand: lint }\n",
	} {
		if err := WriteConfig(ws.Repo, []byte(bad)); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if b, _ := os.ReadFile(config.Path(dir)); string(b) != testConfig {
		t.Fatalf("invalid config written: %q", b)
	}
	good := "version: 0\nbase: main\ngates: {}\n"
	if err := WriteConfig(ws.Repo, []byte(good)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(config.Path(dir)); string(b) != good {
		t.Fatalf("config %q", b)
	}
}

func TestInitConfigRefusesBaseYAMLWouldMisread(t *testing.T) {
	dir := bareRepo(t)
	r, err := gitx.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"&x", "!x", "{x}", "'x", "%x", "|x", "\"x"} {
		if _, err := InitConfig(r, "go", base, true); err == nil {
			t.Errorf("base %q accepted", base)
		}
		if _, err := os.Stat(config.Path(dir)); err == nil {
			t.Fatalf("base %q: config written", base)
		}
	}
	for _, base := range []string{"origin/main", "release/2.1", "x#y"} {
		if _, err := InitConfig(r, "go", base, true); err != nil {
			t.Errorf("base %q: %v", base, err)
		} else if ws, err := Open(dir); err != nil || ws.Config.Base != base {
			t.Errorf("base %q read back as %v (%v)", base, ws, err)
		}
	}
}
