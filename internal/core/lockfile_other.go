//go:build !unix

package core

import "sync"

var lockMu sync.Mutex

// lockFile serialises within this process only: there is no flock here.
func lockFile(string) (func(), error) {
	lockMu.Lock()
	return lockMu.Unlock, nil
}
