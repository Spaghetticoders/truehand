//go:build webui

package truehand

import (
	"embed"
	"io/fs"
)

//go:embed all:web/dist
var webDist embed.FS

// WebDist is the built web app (web/dist), embedded when built with -tags webui.
func WebDist() fs.FS {
	sub, err := fs.Sub(webDist, "web/dist")
	if err != nil {
		panic(err)
	}
	return sub
}
