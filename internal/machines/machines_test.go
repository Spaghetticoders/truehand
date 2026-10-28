package machines

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSSH stands in for ssh: it skips the options, takes the target and runs the command
// here, as the "remote" user whose home is PEER_HOME. Two targets fail like real ssh does.
const fakeSSH = `#!/bin/sh
while [ $# -gt 0 ]; do
  case "$1" in
    -o) printf '%s\n' "$2" >> "$PEER_HOME/ssh-options"; shift 2 ;;
    -*) shift ;;
    *) break ;;
  esac
done
target=$1; shift
case "$target" in
  down.example) echo "ssh: connect to host down.example port 22: Connection refused" >&2; exit 255 ;;
  nokey.example) echo "dev@nokey.example: Permission denied (publickey)." >&2; exit 255 ;;
esac
exec env HOME="$PEER_HOME" SHELL="$PEER_SHELL" PATH="$PEER_PATH" /bin/sh -c "$1"
`

// fakeLoginShell runs `-lc script` without reading any profile.
const fakeLoginShell = `#!/bin/sh
[ "$1" = "-lc" ] && shift
exec /bin/sh -c "$1"
`

func fake(t *testing.T) (*SSH, string) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	bin := filepath.Join(dir, "bin")
	os.MkdirAll(home, 0o755)
	os.MkdirAll(bin, 0o755)
	os.WriteFile(filepath.Join(dir, "ssh"), []byte(fakeSSH), 0o755)
	os.WriteFile(filepath.Join(dir, "loginsh"), []byte(fakeLoginShell), 0o755)
	os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PEER_HOME", home)
	t.Setenv("PEER_SHELL", filepath.Join(dir, "loginsh"))
	t.Setenv("PEER_PATH", bin+":/usr/bin:/bin")
	return &SSH{Bin: filepath.Join(dir, "ssh"), ControlDir: filepath.Join(dir, "cm")}, home
}

func TestArgsAndQuoting(t *testing.T) {
	c := &SSH{Bin: "ssh", ControlDir: "/h/.truehand/ssh"}
	args := strings.Join(c.args("mini", "x"), " ")
	for _, want := range []string{"BatchMode=yes", "ControlMaster=auto", "ControlPath=/h/.truehand/ssh/%C", "ControlPersist=10m", "-T mini x"} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %q in %s", want, args)
		}
	}
	if got := Quote(`it's "x" $HOME`); got != `'it'\''s "x" $HOME'` {
		t.Fatal(got)
	}
	if got := LoginShell("echo hi"); got != `exec "${SHELL:-/bin/sh}" -lc 'echo hi'` {
		t.Fatal(got)
	}
}

func TestProbeInstallAndErrors(t *testing.T) {
	c, home := fake(t)
	ctx := context.Background()
	info, err := c.Probe(ctx, "mini")
	if err != nil {
		t.Fatal(err)
	}
	if info.OS == "" || info.Arch == "" || !info.Tools["claude"] || !info.Tools["git"] || info.Version != "" {
		t.Fatalf("probe: %+v", info)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "ssh-options")); !strings.Contains(string(b), "BatchMode=yes") {
		t.Fatal("ssh must never ask for a password")
	}
	bin := filepath.Join(t.TempDir(), "truehand")
	os.WriteFile(bin, []byte("#!/bin/sh\n[ \"$1\" = version ] && echo 9.9.9\n"), 0o755)
	if err := c.Install(ctx, "mini", bin); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(home, ".truehand", "bin", "truehand"))
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("installed binary: %v %v", fi, err)
	}
	if left, _ := filepath.Glob(filepath.Join(home, ".truehand", "bin", ".truehand.new")); len(left) != 0 {
		t.Fatal("temporary copy left behind")
	}
	if info, _ := c.Probe(ctx, "mini"); info.Version != "9.9.9" {
		t.Fatalf("installed version: %+v", info)
	}
	entries, _ := os.ReadDir(home)
	for _, e := range entries {
		if e.Name() != ".truehand" && e.Name() != "ssh-options" {
			t.Errorf("installed outside ~/.truehand: %s", e.Name())
		}
	}
	if _, err := c.Probe(ctx, "nokey.example"); !errors.Is(err, ErrAuth) {
		t.Fatalf("missing key: %v", err)
	}
	if _, err := c.Probe(ctx, "down.example"); err == nil || errors.Is(err, ErrAuth) || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("host down: %v", err)
	}
}

func TestBinaryFor(t *testing.T) {
	if _, err := BinaryFor("linux", "arm64"); err != nil && !strings.Contains(err.Error(), "make dist") {
		t.Fatalf("a missing cross build must say how to make it: %v", err)
	}
}

func TestProbeAcceptsOnlyKnownPlatforms(t *testing.T) {
	if info, err := parseProbe([]byte("Linux\naarch64\ntool git\nversion 1.2\n")); err != nil || info.OS != "linux" || info.Arch != "arm64" || !info.Tools["git"] || info.Version != "1.2" {
		t.Fatalf("%+v %v", info, err)
	}
	for _, bad := range []string{"x/../../Users/e/.ssh\nid_ed25519\n", "Darwin\n../x\n", "Windows_NT\nx86_64\n", "Darwin\n"} {
		if _, err := parseProbe([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
