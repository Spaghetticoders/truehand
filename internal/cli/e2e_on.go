//go:build truehand_e2e

package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"os/user"
	"strconv"
	"syscall"
	"time"

	"github.com/spaghetticoders/truehand/internal/appstate"
	"github.com/spaghetticoders/truehand/internal/humanauth"
	"github.com/spaghetticoders/truehand/internal/serve"
)

func init() {
	e2eBuild = true
	e2eStart = e2eCheckHome
	humanauth.Authenticate = e2eAuthenticate
	serve.AuthMethod = func() string { return "e2e-fake-auth" }
	e2eBootstrap = e2eAnchorBootstrap
	if d := e2eAnchorDir(); d != "" {
		appstate.AnchorPath = filepath.Join(d, "anchor.json")
		appstate.AnchorFileOK = e2eAnchorFileOK
	}
	serve.ProjectAllowed = func(root string) error {
		if !underTemp(root) {
			return fmt.Errorf("the E2E build registers only repos under a temporary directory, not %s", root)
		}
		return nil
	}
}

func underTemp(p string) bool {
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return false
	}
	for _, root := range []string{"/tmp", "/private/tmp", os.TempDir()} {
		if r, err := filepath.EvalSymlinks(root); err == nil && strings.HasPrefix(real, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// e2eAuthenticate stands in for the OS dialog: tests run with nobody at the Mac.
func e2eAuthenticate(reason string) error {
	fmt.Fprintf(os.Stderr, "E2E: OS authentication skipped: %s\n", reason)
	return nil
}

// e2eCheckHome keeps this build away from real state: it runs only with HOME under a
// temporary directory.
func e2eCheckHome(w io.Writer) error {
	fmt.Fprintln(w, "*** E2E BUILD — NOT FOR USE: no agent guard, fake OS authentication ***")
	if underTemp(os.Getenv("HOME")) {
		return nil
	}
	return errors.New("the E2E build runs only with HOME under a temporary directory (/tmp or $TMPDIR)")
}

// e2eAnchorFileOK stands in for "owned by root": the user's own file, not writable by others.
func e2eAnchorFileOK(fi os.FileInfo) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Getuid() || fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s: E2E anchor must be this user's, not writable by others", fi.Name())
	}
	return nil
}

type stderrTTY struct{}

func (stderrTTY) Write(b []byte) (int, error) { return os.Stderr.Write(b) }
func (stderrTTY) Close() error                { return nil }

// e2eAnchorBootstrap runs the bootstrap as this user against TRUEHAND_E2E_ANCHOR_DIR, the
// code on stderr.
func e2eAnchorBootstrap(env *bootstrapEnv) error {
	d := e2eAnchorDir()
	if d == "" {
		return errors.New("the E2E build bootstraps only with TRUEHAND_E2E_ANCHOR_DIR under a temporary directory")
	}
	uid := strconv.Itoa(os.Getuid())
	*env = bootstrapEnv{
		euid: 0, sudoUID: uid, sudoUser: os.Getenv("USER"),
		tty:       func() (io.WriteCloser, error) { return stderrTTY{}, nil },
		sudoNoPwd: func(*user.User) bool { return false },
		lookup: func(string) (*user.User, error) {
			return &user.User{Uid: uid, Gid: strconv.Itoa(os.Getgid()), Username: "e2e", HomeDir: os.Getenv("HOME")}, nil
		},
		anchor: filepath.Join(d, "anchor.json"),
		writeFile: func(p string, b []byte) error {
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			return appstate.WriteFileAtomic(p, b)
		},
		now: time.Now, sleep: time.Sleep, ttl: 10 * time.Minute, host: env.host, dirOK: e2eAnchorFileOK,
		confirm: func(q string) (bool, error) {
			fmt.Fprint(os.Stderr, q)
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			return strings.TrimSpace(line) == "yes", nil
		},
	}
	return nil
}

// e2eAnchorDir is TRUEHAND_E2E_ANCHOR_DIR, made if missing, when it is under a temporary
// directory; "" otherwise.
func e2eAnchorDir() string {
	d := os.Getenv("TRUEHAND_E2E_ANCHOR_DIR")
	if d == "" || !filepath.IsAbs(d) {
		return ""
	}
	_ = os.MkdirAll(d, 0o700)
	if !underTemp(d) {
		return ""
	}
	return d
}
