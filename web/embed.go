// Package web embeds the compiled frontend (Vue/Vite build output) so the
// application ships as a single self-contained binary with no external assets.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// DistFS returns the embedded frontend rooted at the "dist" directory.
func DistFS() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}
