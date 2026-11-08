package appstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// root's revocation list counts only as a root-owned regular file: one the user owns, or a
// symlink, is an error (and an error trusts no owner), never an empty list.
func TestReviewRevokedListMustBeRoots(t *testing.T) {
	dir := t.TempDir()
	old, oldOK := AnchorPath, AnchorFileOK
	t.Cleanup(func() { AnchorPath, AnchorFileOK = old, oldOK })
	AnchorPath = filepath.Join(dir, "truehand", "anchor.json")
	os.MkdirAll(filepath.Dir(AnchorPath), 0o755)
	// The directory passes; only the file's own check is under test.
	AnchorFileOK = func(fi os.FileInfo) error {
		if fi.IsDir() {
			return nil
		}
		return rootOwned(fi)
	}
	b, _ := json.Marshal(RevokedList{Version: 1, UID: os.Getuid()})
	os.WriteFile(RevokedPath(), b, 0o644)
	if os.Getuid() != 0 {
		if _, err := ReadRevoked(os.Getuid()); err == nil {
			t.Fatal("a revocation list the user owns was read as root's")
		}
	}
	AnchorFileOK = func(os.FileInfo) error { return nil }
	os.Rename(RevokedPath(), RevokedPath()+".real")
	os.Symlink(RevokedPath()+".real", RevokedPath())
	if _, err := ReadRevoked(os.Getuid()); err == nil {
		t.Fatal("a symlinked revocation list was followed")
	}
}
