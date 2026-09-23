package httpserver

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ZenNotes/znserver"
	"github.com/ZenNotes/znserver/internal/config"
)

// Both endpoints name the release the startup log names, under /api and the
// legacy root mount alike, rather than a placeholder a bug report would repeat.
func TestVersionEndpointsReportTheRelease(t *testing.T) {
	root := t.TempDir()
	server, _ := newTestServer(t, config.Config{
		VaultPath:        root,
		DefaultVaultPath: root,
		Bind:             "127.0.0.1:7878",
		BrowseRoots:      []string{root},
	})

	for _, prefix := range []string{"/api", ""} {
		for _, path := range []string{"/version", "/capabilities"} {
			resp, err := http.Get(server.URL + prefix + path)
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				Version string `json:"version"`
			}
			err = json.NewDecoder(resp.Body).Decode(&body)
			resp.Body.Close()
			if err != nil {
				t.Fatalf("decode %s%s: %v", prefix, path, err)
			}
			if body.Version != znserver.Version() {
				t.Fatalf("GET %s%s version = %q, want %q", prefix, path, body.Version, znserver.Version())
			}
		}
	}
}
