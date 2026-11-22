// Package webui holds the built server web panel (dist/, made by
// `npm run build` here or by the node stage of the server image) for the
// embed directive below. Without a build only dist/.gitkeep is there and the panel
// serves a placeholder page; the API works either way.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Assets returns the built panel files (index.html, assets/).
func Assets() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil
	}
	return sub
}
