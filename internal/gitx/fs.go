package gitx

import (
	"os"
	"path/filepath"
)

func fileExists(root, rel string) bool {
	fi, err := os.Lstat(filepath.Join(root, rel))
	return err == nil && !fi.IsDir()
}
