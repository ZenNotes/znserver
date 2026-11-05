package httpserver

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZenNotes/znserver/internal/config"
)

// The web client shows a vault's PDFs in an iframe served by this same
// server, and the PDF response carries the policy too. `frame-ancestors
// 'none'` therefore forbade the app's own same-origin frame and every PDF
// embed rendered as a blocked frame (#121). Same-origin framing must stay
// allowed; framing by other sites stays blocked.
func TestContentSecurityPolicyAllowsSameOriginFraming(t *testing.T) {
	csp := contentSecurityPolicy("notes.example.com")
	if !strings.Contains(csp, "frame-ancestors 'self'") {
		t.Fatalf("CSP must allow same-origin framing for PDF embeds, got: %s", csp)
	}
	if strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("CSP still forbids same-origin framing: %s", csp)
	}
}

func directive(csp, name string) string {
	for _, part := range strings.Split(csp, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, name+" ") {
			return part
		}
	}
	return ""
}

// connect-src is where the page's own script may send anything. It names the
// page's origin, the watch socket on the page's host, and the one other API the
// web client calls (Unsplash, for covers), not every https and ws host.
func TestConnectSrcIsScopedToThePagesOwnHost(t *testing.T) {
	root := t.TempDir()
	server, _ := newTestServer(t, config.Config{VaultPath: root, Bind: "127.0.0.1:7878"})
	resp, err := http.Get(server.URL + "/api/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	want := "connect-src 'self' ws://" + host + " wss://" + host + " https://api.unsplash.com"
	if got := directive(resp.Header.Get("Content-Security-Policy"), "connect-src"); got != want {
		t.Fatalf("connect-src = %q, want %q", got, want)
	}

	// The vault's raw files carry the same scope, plus their sandbox.
	if err := os.MkdirAll(filepath.Join(root, "inbox"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "inbox", "pic.png"), []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	asset, err := http.Get(server.URL + "/api/assets/raw?path=inbox/pic.png")
	if err != nil {
		t.Fatal(err)
	}
	asset.Body.Close()
	csp := asset.Header.Get("Content-Security-Policy")
	if got := directive(csp, "connect-src"); got != want {
		t.Errorf("raw asset connect-src = %q, want %q", got, want)
	}
	if !strings.HasSuffix(csp, "; sandbox") {
		t.Errorf("raw asset lost its sandbox: %s", csp)
	}
}

func TestConnectSrcNamesNoWildcardScheme(t *testing.T) {
	got := directive(contentSecurityPolicy("notes.example.com"), "connect-src")
	for _, token := range strings.Fields(got)[1:] {
		if strings.HasSuffix(token, ":") {
			t.Errorf("connect-src allows the whole %s scheme: %q", token, got)
		}
	}
	if want := "connect-src 'self' ws://notes.example.com wss://notes.example.com https://api.unsplash.com"; got != want {
		t.Errorf("connect-src = %q, want %q", got, want)
	}
}

// Host comes from the client. One that is not a plain host[:port] must not be
// able to add sources or directives, so it names no socket and 'self' stands.
func TestConnectSrcIgnoresAHostThatIsNotOne(t *testing.T) {
	for _, host := range []string{"", "evil.example; script-src *", "a b", "x'y", "[::1]:7878\r\n"} {
		got := directive(contentSecurityPolicy(host), "connect-src")
		if got != "connect-src 'self' https://api.unsplash.com" {
			t.Errorf("host %q: connect-src = %q", host, got)
		}
	}
	if got := directive(contentSecurityPolicy("[::1]:7878"), "connect-src"); !strings.Contains(got, "ws://[::1]:7878") {
		t.Errorf("an IPv6 host names its socket: %q", got)
	}
}
