//go:build !webui

package truehand

import "io/fs"

// WebDist is nil without -tags webui, so `go build` and `go install` work from a checkout
// where web/dist was never built; the server then shows a page saying how to build it.
func WebDist() fs.FS { return nil }
