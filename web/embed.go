//go:build embed_web

package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist dist/index.html
var dist embed.FS

// Dist returns the embedded PWA bundle. Production builds require index.html
// at compile time so an incomplete asset bundle cannot produce a release binary.
func Dist() (fs.FS, error) {
	return fs.Sub(dist, "dist")
}
