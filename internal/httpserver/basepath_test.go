package httpserver

import (
	"bytes"
	"io/fs"
	"net/http"
	"net/http/httptest"
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
		"manifest.webmanifest": &fstest.MapFile{Data: []byte("{}")},
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
