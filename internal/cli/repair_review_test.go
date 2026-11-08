package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// revoked.json as a link: --repair runs as root, so following it would copy whatever root can
// read (/etc/shadow) into the world-readable .bak. It must refuse a link, not read through it.
func TestReviewRepairDoesNotFollowALink(t *testing.T) {
	env, _, u := testBootstrapEnv(t)
	u.Uid = strconv.Itoa(os.Getuid())
	path := filepath.Join(filepath.Dir(env.anchor), "revoked.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	secret := filepath.Join(t.TempDir(), "shadow")
	os.WriteFile(secret, []byte("root:$6$secrethash:19000:0:99999:7:::\n"), 0o600)
	os.Symlink(secret, path)
	var c bytes.Buffer
	err := repairRevoked(&ctx{stdout: &c, stderr: &c}, *env)
	if bak, _ := os.ReadFile(path + ".bak"); strings.Contains(string(bak), "secrethash") {
		t.Fatal("--repair copied the link's target into revoked.json.bak")
	}
	if err == nil {
		t.Fatal("--repair accepted a revocation list that is a link")
	}
	if b, _ := os.ReadFile(secret); !strings.Contains(string(b), "secrethash") {
		t.Fatal("--repair wrote through the link")
	}
}

// The anchor directory itself: a link or a directory others can write is refused by
// bootstrap, revoke and --repair, before anything is made, chowned or written.
func TestAnchorDirMustBeRootsDirectory(t *testing.T) {
	var out bytes.Buffer
	c := &ctx{stdout: &out, stderr: &out}
	runs := map[string]func(bootstrapEnv) error{
		"bootstrap": func(e bootstrapEnv) error { return bootstrap(c, e) },
		"revoke":    func(e bootstrapEnv) error { return revoke(c, e, "x") },
		"repair":    func(e bootstrapEnv) error { return repairRevoked(c, e) },
	}
	for name, run := range runs {
		env, _, u := testBootstrapEnv(t)
		u.Uid = strconv.Itoa(os.Getuid())
		dir := filepath.Dir(env.anchor)
		elsewhere := t.TempDir()
		os.MkdirAll(filepath.Dir(dir), 0o755)
		os.Symlink(elsewhere, dir)
		os.WriteFile(filepath.Join(elsewhere, "revoked.json"), []byte("garbage"), 0o644)
		if err := run(*env); err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Errorf("%s with the directory a link = %v", name, err)
		}
		if entries, _ := os.ReadDir(elsewhere); len(entries) != 1 {
			t.Errorf("%s wrote through the directory link: %v", name, entries)
		}

		env, _, u = testBootstrapEnv(t)
		u.Uid = strconv.Itoa(os.Getuid())
		dir = filepath.Dir(env.anchor)
		os.MkdirAll(dir, 0o777)
		os.Chmod(dir, 0o777)
		env.dirOK = func(fi os.FileInfo) error {
			if fi.Mode().Perm()&0o022 != 0 {
				return os.ErrPermission
			}
			return nil
		}
		if err := run(*env); err == nil || !strings.Contains(err.Error(), "nothing was changed") {
			t.Errorf("%s with a directory others can write = %v", name, err)
		}
		if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o777 {
			t.Errorf("%s changed the directory's mode", name)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("%s wrote into it: %v", name, entries)
		}
	}
}
