package httpserver

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// MCP clients look for OAuth metadata before using a plain token. The app's page is
// not metadata, so those URLs must 404 while app routes keep their fallback (#56).
func TestWellKnownMissesAreNotFound(t *testing.T) {
	for _, base := range []string{"", "/zennotes"} {
		srv := newBasePathServer(t, base)
		for _, path := range []string{
			"/.well-known/oauth-protected-resource/mcp",
			"/.well-known/oauth-protected-resource",
			"/.well-known/oauth-authorization-server",
			"/.well-known/openid-configuration",
			"/.well-known",
		} {
			resp, err := http.Get(srv.URL + base + path)
			if err != nil {
				t.Fatalf("get %s%s: %v", base, path, err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("%s%s: status %d, want 404", base, path, resp.StatusCode)
			}
			if strings.Contains(string(body), "<html") {
				t.Fatalf("%s%s: got the app page", base, path)
			}
		}
		for _, path := range []string{"/", "/notes/inbox/Hello.md", "/settings"} {
			resp, err := http.Get(srv.URL + base + path)
			if err != nil {
				t.Fatalf("get %s%s: %v", base, path, err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "<html") {
				t.Fatalf("%s%s: status %d, want the app page", base, path, resp.StatusCode)
			}
		}
	}
}
