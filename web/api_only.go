//go:build !embed_web

package web

import (
	"errors"
	"io/fs"
)

// Dist allows Go development and tests without a frontend toolchain or assets.
// Distribution builds select embed_web and include the pinned browser bundle.
func Dist() (fs.FS, error) {
	return nil, errors.New("web bundle not embedded: use Vite in development or build with -tags embed_web")
}
