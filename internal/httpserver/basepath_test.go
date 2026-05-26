package httpserver

import (
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ZenNotes/zennotes/apps/server/internal/config"
	"github.com/ZenNotes/zennotes/apps/server/internal/vault"
)

func newBasePathServer(t *testing.T, basePath string) *httptest.Server {
	t.Helper()
	cfg := config.Config{
		VaultPath:           t.TempDir(),
		Bind:                "127.0.0.1:0",
		BasePath:            basePath,
		AllowInsecureNoAuth: true,
	}
	v, err := vault.New(cfg.VaultPath, vault.Options{})
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	static := fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte("<!doctype html><html><head><title>ZenNotes</title></head><body></body></html>"),
		},
		"manifest.webmanifest":  &fstest.MapFile{Data: []byte("{}")},
		"assets/index-test.css": &fstest.MapFile{Data: []byte("body{color:red}")},
	}
	srv := httptest.NewServer(New(v, nil, fs.FS(static), cfg).Router())
	t.Cleanup(srv.Close)
	return srv
}

func TestBasePathHealthz(t *testing.T) {
	srv := newBasePathServer(t, "/zennotes")
	resp, err := http.Get(srv.URL + "/zennotes/api/healthz")
	if err != nil {
		t.Fatalf("get under base: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status under base: %d", resp.StatusCode)
	}

	off, err := http.Get(srv.URL + "/api/healthz")
	if err != nil {
		t.Fatalf("get without base: %v", err)
	}
	defer off.Body.Close()
	if off.StatusCode == http.StatusOK {
		t.Fatalf("requests outside the base path should not match: got 200")
	}
}

func TestBasePathServesStaticAssets(t *testing.T) {
	srv := newBasePathServer(t, "/zennotes")

	// A hashed CSS asset under the base path must serve the real file
	// with a CSS content type. If the prefix isn't stripped before the
	// embedded-FS lookup, serveStatic falls back to index.html and the
	// browser refuses it for a bad MIME type (issue #58).
	resp, err := http.Get(srv.URL + "/zennotes/assets/index-test.css")
	if err != nil {
		t.Fatalf("get css under base: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Fatalf("expected text/css content type, got %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "body{color:red}" {
		t.Fatalf("expected css body, got %q", string(body))
	}
}

func TestBasePathServesManifest(t *testing.T) {
	srv := newBasePathServer(t, "/zennotes")
	resp, err := http.Get(srv.URL + "/zennotes/manifest.webmanifest")
	if err != nil {
		t.Fatalf("get manifest under base: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "{}" {
		t.Fatalf("expected manifest body {}, got %q", string(body))
	}
}

func TestBasePathInjectsRuntimeHint(t *testing.T) {
	srv := newBasePathServer(t, "/zennotes")
	resp, err := http.Get(srv.URL + "/zennotes/")
	if err != nil {
		t.Fatalf("get root: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	if !bytes.Contains(body[:n], []byte(`<meta name="zn-base-path" content="/zennotes">`)) {
		t.Fatalf("expected base path meta tag in index.html, got:\n%s", string(body[:n]))
	}
}

func TestRootDeploymentHasNoBasePathHint(t *testing.T) {
	srv := newBasePathServer(t, "")
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("get root: %v", err)
	}
	defer resp.Body.Close()
	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	if bytes.Contains(body[:n], []byte("zn-base-path")) {
		t.Fatalf("root deployment should not inject base path meta, got:\n%s", string(body[:n]))
	}
}
