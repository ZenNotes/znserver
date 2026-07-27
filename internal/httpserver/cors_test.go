package httpserver

import (
	"net/http"
	"testing"

	"github.com/ZenNotes/zennotes/apps/server/internal/config"
)

// corsHeaders returns what the middleware answers for a given Origin.
func corsHeaders(t *testing.T, cfg config.Config, origin string) http.Header {
	t.Helper()
	root := t.TempDir()
	cfg.VaultPath = root
	cfg.DefaultVaultPath = root
	if cfg.Bind == "" {
		// A non-loopback bind, so the loopback exemption never masks the
		// behaviour under test.
		cfg.Bind = "192.0.2.10:7878"
	}
	server, _ := newTestServer(t, cfg)

	req, err := http.NewRequest(http.MethodOptions, server.URL+"/api/capabilities", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Origin", origin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("preflight for %q: %v", origin, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp.Header
}

func TestNormalizeOrigin(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		// Origins a browser or WebView actually sends.
		{"https://notes.example.com", "https://notes.example.com"},
		{"HTTPS://Notes.Example.COM", "https://notes.example.com"},
		{"http://localhost:5173", "http://localhost:5173"},
		{"app://.", "app://."},
		{"capacitor://localhost", "capacitor://localhost"},
		// Opaque and scheme-only origins were dropped before #482, so an
		// operator could list them and still be rejected.
		{"null", "null"},
		{"NULL", "null"},
		{"file://", "file://"},
		{"*", "*"},
		// Still not origins.
		{"", ""},
		{"   ", ""},
		{"notes.example.com", ""},
		{"mailto:someone@example.com", ""},
	}
	for _, tc := range cases {
		if got := normalizeOrigin(tc.raw); got != tc.want {
			t.Errorf("normalizeOrigin(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestCORSAllowsExplicitlyListedOpaqueOrigins(t *testing.T) {
	// Listing these verbatim is what the docs and operators do; before #482
	// they were parsed away and rejected anyway.
	for _, origin := range []string{"null", "file://", "app://."} {
		cfg := config.Config{AuthToken: "secret-token", AllowedOrigins: []string{"null", "file://", "app://."}}
		headers := corsHeaders(t, cfg, origin)
		if got := headers.Get("Access-Control-Allow-Origin"); got != origin {
			t.Errorf("origin %q: Allow-Origin = %q, want %q", origin, got, origin)
		}
		if got := headers.Get("Access-Control-Allow-Credentials"); got != "true" {
			t.Errorf("origin %q: an explicitly listed origin keeps credentials, got %q", origin, got)
		}
	}
}

func TestCORSWildcardAllowsAnyOriginWithoutCredentials(t *testing.T) {
	cfg := config.Config{AuthToken: "secret-token", AllowedOrigins: []string{"*"}}
	for _, origin := range []string{"https://anything.example", "null", "file://", "app://."} {
		headers := corsHeaders(t, cfg, origin)
		if got := headers.Get("Access-Control-Allow-Origin"); got != origin {
			t.Errorf("wildcard: Allow-Origin for %q = %q, want the origin echoed", origin, got)
		}
		// Echoing any origin *and* allowing credentials would let any site a
		// user visits drive their session cookie.
		if got := headers.Get("Access-Control-Allow-Credentials"); got != "" {
			t.Errorf("wildcard: credentials must be withheld for %q, got %q", origin, got)
		}
	}
}

func TestCORSRejectsUnlistedOriginByDefault(t *testing.T) {
	cfg := config.Config{AuthToken: "secret-token"}
	headers := corsHeaders(t, cfg, "https://evil.example")
	if got := headers.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("unlisted origin must not be echoed, got %q", got)
	}
}

func TestCORSAllowsSameOriginWithCredentials(t *testing.T) {
	// The server's own web bundle: same origin as the request, always allowed.
	root := t.TempDir()
	server, _ := newTestServer(t, config.Config{
		VaultPath:        root,
		DefaultVaultPath: root,
		Bind:             "192.0.2.10:7878",
		AuthToken:        "secret-token",
	})
	req, err := http.NewRequest(http.MethodOptions, server.URL+"/api/capabilities", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Origin", "http://"+req.Host)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("same-origin keeps credentials, got %q", got)
	}
}

func TestCORSPreflightShortCircuits(t *testing.T) {
	cfg := config.Config{AuthToken: "secret-token", AllowedOrigins: []string{"https://notes.example.com"}}
	root := t.TempDir()
	cfg.VaultPath = root
	cfg.DefaultVaultPath = root
	cfg.Bind = "192.0.2.10:7878"
	server, _ := newTestServer(t, cfg)

	req, err := http.NewRequest(http.MethodOptions, server.URL+"/api/capabilities", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Origin", "https://notes.example.com")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("preflight status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
}
