// Package znserver carries the release metadata in release.json into every build.
package znserver

import (
	_ "embed"
	"encoding/json"
	"strings"
)

// Compiled in rather than read at startup: the Docker image and the release
// binaries ship without release.json, and a relative read would depend on the
// directory the server was launched from.
//
//go:embed release.json
var releaseJSON []byte

// Version is the version field of release.json at build time. It returns
// "undefined" instead of failing when that field is missing or malformed.
func Version() string {
	return versionFrom(releaseJSON)
}

func versionFrom(raw []byte) string {
	var release struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &release); err == nil {
		if version := strings.TrimSpace(release.Version); version != "" {
			return version
		}
	}
	return "undefined"
}
