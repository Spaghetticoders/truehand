package appstate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestFilesArePrivateAndAtomic(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "app")
	os.MkdirAll(dir, 0o755)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v, want 0700", fi.Mode().Perm())
	}
	if _, err := s.AddProject("/tmp/x/demo", "Acme", ""); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "state.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("state.json: %v %v", fi, err)
	}
	left, _ := filepath.Glob(filepath.Join(dir, ".state.json.*"))
	if len(left) != 0 {
		t.Fatalf("temporary files left behind: %v", left)
	}
}

func TestProjects(t *testing.T) {
	s, _ := Open(t.TempDir())
	p, err := s.AddProject("/work/acme/app/", "Acme", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != ProjectID("/work/acme/app") || !strings.HasPrefix(p.ID, "p_") || len(p.ID) != 10 || p.Name != "app" {
		t.Fatalf("unexpected project %+v", p)
	}
	if _, err := s.AddProject("/work/acme/app", "Other", "x"); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate root must be refused, got %v", err)
	}
	if _, err := s.AddProject("rel/path", "", ""); err == nil {
		t.Fatal("relative roots must be refused")
	}
	st, _ := s.State()
	if len(st.Projects) != 1 || st.Settings.DefaultPermissionMode != "default" || st.Settings.Lang != "it" || st.Settings.Remote.Mode != "off" {
		t.Fatalf("unexpected state %+v", st)
	}
	if err := s.RemoveProject(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveProject(p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestFailedUpdateWritesNothing(t *testing.T) {
	s, _ := Open(t.TempDir())
	s.AddProject("/a", "", "")
	_ = s.UpdateState(func(st *State) error {
		st.Projects = nil
		return errors.New("no")
	})
	st, _ := s.State()
	if len(st.Projects) != 1 {
		t.Fatal("a failed update must not be written")
	}
}

func TestConcurrentUpdatesAreSerialized(t *testing.T) {
	s, _ := Open(t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.PutSession(Session{ID: NewID("s_"), Status: StatusStarting})
		}()
	}
	wg.Wait()
	list, _ := s.Sessions()
	if len(list) != 20 {
		t.Fatalf("lost updates: %d sessions", len(list))
	}
}

func TestSecretsAreStoredHashed(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	tok := NewSecret()
	if err := s.SetToken(tok); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "auth.json"))
	if strings.Contains(string(b), tok) {
		t.Fatal("token written in clear")
	}
	a, _ := s.Auth()
	if !MatchSecret(a.TokenSHA256, tok) || MatchSecret(a.TokenSHA256, tok+"x") || MatchSecret("", "") || MatchSecret(a.TokenSHA256, "") {
		t.Fatal("MatchSecret")
	}
}

func TestSettingsValidation(t *testing.T) {
	ok := Settings{Lang: "en", Remote: Remote{Mode: "tailnet"}, DefaultPermissionMode: "plan"}
	if err := ValidateSettings(ok); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Settings{
		{Lang: "fr", Remote: Remote{Mode: "off"}, DefaultPermissionMode: "default"},
		{Lang: "it", Remote: Remote{Mode: "public"}, DefaultPermissionMode: "default"},
		{Lang: "it", Remote: Remote{Mode: "off"}, DefaultPermissionMode: "bypassPermissions"},
	} {
		if ValidateSettings(bad) == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestPluginDirsInUse(t *testing.T) {
	s, _ := Open(t.TempDir())
	s.PutSession(Session{ID: "a", Status: StatusWorking, PluginDir: "/p/1"})
	s.PutSession(Session{ID: "b", Status: StatusExited, PluginDir: "/p/2", Resumable: true})
	s.PutSession(Session{ID: "c", Status: StatusExited, PluginDir: "/p/3"})
	in, _ := s.PluginDirsInUse()
	if !in["/p/1"] || !in["/p/2"] || in["/p/3"] {
		t.Fatalf("%v", in)
	}
}

func TestLock(t *testing.T) {
	s, _ := Open(t.TempDir())
	release, err := s.Lock()
	if err != nil {
		t.Fatal(err)
	}
	// flock is per open file description, so a second open in the same process conflicts.
	if _, err := s.Lock(); !errors.Is(err, ErrLocked) {
		t.Fatalf("second lock: %v", err)
	}
	release()
	r2, err := s.Lock()
	if err != nil {
		t.Fatal(err)
	}
	r2()
}

func TestScrollbackPathTakesOnlySessionIDs(t *testing.T) {
	s, _ := Open(t.TempDir())
	if p, err := s.ScrollbackPath("s_0a1b"); err != nil || filepath.Base(p) != "s_0a1b.log" {
		t.Fatal(p, err)
	}
	for _, bad := range []string{"", "../auth", "s_../../x", "s_ZZ", "x"} {
		if _, err := s.ScrollbackPath(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestMachines(t *testing.T) {
	s, _ := Open(t.TempDir())
	m, err := s.AddMachine(Machine{SSHTarget: "dev@mini.example-tail.ts.net", Label: " Mac mini "})
	if err != nil || !strings.HasPrefix(m.ID, "m_") || len(m.ID) != 10 || m.Label != "Mac mini" {
		t.Fatalf("%+v %v", m, err)
	}
	if _, err := s.AddMachine(Machine{SSHTarget: m.SSHTarget}); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	for _, bad := range []string{"", "-oProxyCommand=sh", "host;rm -rf", "a b", "$(x)", "host:22"} {
		if _, err := s.AddMachine(Machine{SSHTarget: bad}); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	st, _ := s.State()
	if x, ok := st.Machine(m.ID); !ok || x.Label != "Mac mini" {
		t.Fatal("lookup")
	}
	if err := s.RemoveMachine(m.ID); err != nil {
		t.Fatal(err)
	}
}

func TestSocketPathFitsAUnixSocket(t *testing.T) {
	if p, err := SocketPath("/Users/me/.truehand/app"); err != nil || p != "/Users/me/.truehand/app/serve.sock" {
		t.Fatal(p, err)
	}
	long := "/" + strings.Repeat("x", 120) + "/app"
	p, err := SocketPath(long)
	if err != nil || len(p) > maxSocketPath || !strings.HasPrefix(p, "/tmp/truehand-") {
		t.Fatal(p, err)
	}
	defer os.RemoveAll(filepath.Dir(p))
	if again, _ := SocketPath(long); again != p {
		t.Fatal("server and clients must agree on the path")
	}
	os.Chmod(filepath.Dir(p), 0o777)
	if _, err := SocketPath(long); err == nil {
		t.Fatal("a directory opened to others must be refused")
	}
}
