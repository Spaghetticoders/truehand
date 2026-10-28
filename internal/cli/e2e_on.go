//go:build truehand_e2e

package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spaghetticoders/truehand/internal/humanauth"
	"github.com/spaghetticoders/truehand/internal/serve"
)

func init() {
	e2eBuild = true
	e2eStart = e2eCheckHome
	humanauth.Authenticate = e2eAuthenticate
	serve.AuthMethod = func() string { return "e2e-fake-auth" }
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
