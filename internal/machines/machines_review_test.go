package machines

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// os and arch come from the remote machine's uname: a hostile peer must not pick which local
// file the Mac uploads to it.
func TestReviewBinaryForRefusesRemotePaths(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "thsec")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	secret := filepath.Join(dir, "secret-z")
	os.WriteFile(secret, []byte("PRIVATE KEY"), 0o600)
	goos := "x" + strings.Repeat("/..", 40) + dir + "/secret"
	if p, err := BinaryFor(goos, "z"); err == nil {
		t.Fatalf("a peer's uname chose the local file %s to upload", p)
	}
	for _, bad := range [][2]string{{"linux", "../x"}, {"linux/..", "arm64"}, {"windows", "amd64"}, {"", ""}} {
		if p, err := BinaryFor(bad[0], bad[1]); err == nil && !strings.HasSuffix(p, "truehand") {
			t.Errorf("%q/%q accepted: %s", bad[0], bad[1], p)
		}
	}
}
