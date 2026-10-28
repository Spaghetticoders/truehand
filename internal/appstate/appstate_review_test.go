package appstate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReviewAtomicWriteTightensExistingFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(p, []byte(`{"token_sha256":"x"}`)); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", fi.Mode().Perm(), err)
	}
}

func TestReviewOpenTightensSubdirs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "app")
	for _, d := range []string{dir, filepath.Join(dir, "scrollback"), filepath.Join(dir, "hooks")} {
		if err := os.MkdirAll(d, 0o777); err != nil {
			t.Fatal(err)
		}
		os.Chmod(d, 0o777)
	}
	if _, err := Open(dir); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{dir, filepath.Join(dir, "scrollback"), filepath.Join(dir, "hooks")} {
		if fi, _ := os.Stat(d); fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v", d, fi.Mode().Perm())
		}
	}
}

func TestReviewCorruptFileIsNeverOverwritten(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "app"))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(s.Dir, authFile)
	if err := os.WriteFile(p, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.SetToken("tok"); err == nil {
		t.Fatal("update over a corrupt auth.json succeeded")
	}
	if b, _ := os.ReadFile(p); string(b) != "{broken" {
		t.Fatalf("corrupt file replaced: %q", b)
	}
}

func TestReviewTokenNeverStoredInClear(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "app"))
	if err != nil {
		t.Fatal(err)
	}
	tok := NewSecret()
	if err := s.SetToken(tok); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(s.Dir, authFile))
	if len(b) == 0 || containsStr(string(b), tok) {
		t.Fatalf("token in clear: %s", b)
	}
	a, _ := s.Auth()
	if !MatchSecret(a.TokenSHA256, tok) || MatchSecret(a.TokenSHA256, tok[:63]) || MatchSecret(a.TokenSHA256, a.TokenSHA256) {
		t.Fatal("token match wrong")
	}
	if MatchSecret("", "") || MatchSecret(HashSecret(""), "") {
		t.Fatal("empty secret matched")
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestReviewSecretsAreRandom(t *testing.T) {
	a, b := NewSecret(), NewSecret()
	if len(a) != 64 || a == b {
		t.Fatalf("%q %q", a, b)
	}
}

func TestReviewNoBypassPermissionMode(t *testing.T) {
	for _, m := range []string{"bypassPermissions", "dontAsk", "auto", ""} {
		if ValidPermissionMode(m) {
			t.Errorf("%q allowed", m)
		}
		if ValidateSettings(Settings{Lang: "it", Remote: Remote{Mode: "off"}, DefaultPermissionMode: m}) == nil {
			t.Errorf("settings with %q accepted", m)
		}
	}
}

func TestReviewPluginDirsInUse(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "app"))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []Session{
		{ID: "a", PluginDir: "/p/live", Status: StatusIdle},
		{ID: "b", PluginDir: "/p/resumable", Status: StatusCrashed, Resumable: true},
		{ID: "c", PluginDir: "/p/dead", Status: StatusExited},
		{ID: "d", PluginDir: "/p/starting", Status: StatusStarting},
	} {
		if err := s.PutSession(v); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.PluginDirsInUse()
	if err != nil {
		t.Fatal(err)
	}
	if !got["/p/live"] || !got["/p/resumable"] || !got["/p/starting"] || got["/p/dead"] {
		t.Fatalf("%v", got)
	}
}

func TestReviewLockIsExclusiveAndReleased(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "app"))
	if err != nil {
		t.Fatal(err)
	}
	release, err := s.Lock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lock(); err == nil {
		t.Fatal("second lock taken")
	}
	release()
	r2, err := s.Lock()
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	r2()
}
