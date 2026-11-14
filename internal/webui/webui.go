// Package webui embeds the built frontend assets.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// FS returns the embedded frontend files rooted at dist/.
func FS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}

// Built reports whether a real frontend build is embedded.
func Built() bool {
	data, err := fs.ReadFile(distFS, "dist/index.html")
	if err != nil {
		return false
	}
	return len(data) > 0 && string(data) != "placeholder\n"
}
