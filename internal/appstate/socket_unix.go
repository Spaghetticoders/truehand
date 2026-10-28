//go:build unix

package appstate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// maxSocketPath is the room in sockaddr_un.sun_path (104 on macOS, 108 on Linux), less the
// terminating NUL.
const maxSocketPath = 103

// SocketPath is where the server of dir listens: dir/serve.sock, or, when that path is too
// long for a unix socket, serve.sock in a private directory under /tmp named after the user
// and dir, so the server and its clients agree on it.
func SocketPath(dir string) (string, error) {
	p := filepath.Join(dir, "serve.sock")
	if len(p) <= maxSocketPath {
		return p, nil
	}
	h := sha256.Sum256([]byte(dir))
	short := filepath.Join("/tmp", fmt.Sprintf("truehand-%d-%s", os.Getuid(), hex.EncodeToString(h[:])[:12]))
	if err := os.Mkdir(short, 0o700); err != nil && !os.IsExist(err) {
		return "", err
	}
	// /tmp is shared: refuse a directory someone else made or opened up.
	fi, err := os.Lstat(short)
	if err != nil {
		return "", err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || fi.Mode().Perm() != 0o700 || !ok || int(st.Uid) != os.Getuid() {
		return "", fmt.Errorf("%s is not a private directory of this user", short)
	}
	return filepath.Join(short, "serve.sock"), nil
}
