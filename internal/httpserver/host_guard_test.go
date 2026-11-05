package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZenNotes/znserver/internal/config"
)

func newOpenLoopbackServer(t *testing.T, cfg config.Config) (*httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "inbox"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "inbox", "Keep.md"), []byte("# Keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.VaultPath = root
	cfg.DefaultVaultPath = root
	if cfg.Bind == "" {
		cfg.Bind = "127.0.0.1:7878"
	}
	server, _ := newTestServer(t, cfg)
	return server, root
}

// sendAs sends a request to the server with the given Host header, carrying
// the headers a browser sends for a page on that same host.
func sendAs(t *testing.T, server *httptest.Server, host, method, path string, body []byte, extra map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	req.Header.Set("Origin", "http://"+host)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func overwrite(t *testing.T) []byte {
	raw, _ := json.Marshal(map[string]string{"path": "inbox/Keep.md", "body": "overwritten"})
	return raw
}

// A loopback server with no token answers only to names that cannot point
// anywhere else. A DNS name can be made to resolve to 127.0.0.1, and a page on
// it is then same-origin with the server.
func TestOpenLoopbackServerRefusesOtherHostNames(t *testing.T) {
	server, root := newOpenLoopbackServer(t, config.Config{})
	port := server.URL[strings.LastIndex(server.URL, ":")+1:]
	for _, host := range []string{"elsewhere.example:" + port, "ELSEWHERE.example.:" + port, "localhost.elsewhere.example:" + port} {
		if resp := sendAs(t, server, host, http.MethodGet, "/api/notes", nil, nil); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: list status = %d, want 403", host, resp.StatusCode)
		}
		if resp := sendAs(t, server, host, http.MethodPost, "/api/notes/write", overwrite(t), nil); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: write status = %d, want 403", host, resp.StatusCode)
		}
		if resp := sendAs(t, server, host, http.MethodGet, "/api/watch", nil, map[string]string{
			"Connection": "Upgrade", "Upgrade": "websocket", "Sec-WebSocket-Version": "13", "Sec-WebSocket-Key": "dGhlIHNhbXBsZSBub25jZQ==",
		}); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: watch status = %d, want 403", host, resp.StatusCode)
		}
	}
	if raw, _ := os.ReadFile(filepath.Join(root, "inbox", "Keep.md")); string(raw) != "# Keep" {
		t.Fatalf("note changed to %q", raw)
	}
}

func TestOpenLoopbackServerAnswersLocalNames(t *testing.T) {
	server, _ := newOpenLoopbackServer(t, config.Config{AllowedHosts: []string{"Notes.Example.com."}})
	port := server.URL[strings.LastIndex(server.URL, ":")+1:]
	for _, host := range []string{
		"localhost:" + port, "LOCALHOST.:" + port, "localhost", "app.localhost:" + port,
		"127.0.0.1:" + port, "[::1]:" + port, "10.0.2.2:7878", "",
		"notes.example.com", "notes.example.com:443",
	} {
		if resp := sendAs(t, server, host, http.MethodGet, "/api/notes", nil, nil); resp.StatusCode != http.StatusOK {
			t.Errorf("%q: list status = %d, want 200", host, resp.StatusCode)
		}
	}
}

// With a token the token is the check: a page on another name has neither it
// nor a cookie, which the browser keeps per host. Off loopback there is no set
// of names to expect, so the Docker bind is unaffected.
func TestHostCheckOnlyOnAnOpenLoopbackServer(t *testing.T) {
	withToken, _ := newOpenLoopbackServer(t, config.Config{AuthToken: "secret-token"})
	host := "elsewhere.example"
	if resp := sendAs(t, withToken, host, http.MethodGet, "/api/notes", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("token server, no token: status = %d, want 401", resp.StatusCode)
	}
	if resp := sendAs(t, withToken, host, http.MethodGet, "/api/notes", nil, map[string]string{"Authorization": "Bearer secret-token"}); resp.StatusCode != http.StatusOK {
		t.Errorf("token server, bearer: status = %d, want 200", resp.StatusCode)
	}
	docker, _ := newOpenLoopbackServer(t, config.Config{Bind: "0.0.0.0:7878", AllowInsecureNoAuth: true})
	if resp := sendAs(t, docker, "notes.lan:7878", http.MethodGet, "/api/notes", nil, nil); resp.StatusCode != http.StatusOK {
		t.Errorf("0.0.0.0 bind: status = %d, want 200", resp.StatusCode)
	}
}
