// Package webui embeds the built Svelte single-page application so the daemon
// can serve it without any external files.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var assets embed.FS

// FS returns the built SPA assets rooted at the dist directory.
func FS() fs.FS {
	sub, err := fs.Sub(assets, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
