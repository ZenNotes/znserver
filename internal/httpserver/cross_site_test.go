package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZenNotes/znserver/internal/config"
	"github.com/ZenNotes/znserver/internal/vault"
	"github.com/ZenNotes/znserver/internal/watcher"
	"github.com/coder/websocket"
)

const crossSiteToken = "secret-token"

// crossSiteFixture is a running server on a vault holding one note, plus a
// logged-in session cookie, the state a person's browser is in while they use
// the web client.
type crossSiteFixture struct {
	t      *testing.T
	server *httptest.Server
	root   string
	cookie string
}

func newCrossSiteFixture(t *testing.T, cfg config.Config) *crossSiteFixture {
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
	cfg.BrowseRoots = []string{root}
	if cfg.AuthToken == "" {
		cfg.AuthToken = crossSiteToken
	}
	v, err := vault.New(root, vault.Options{})
	if err != nil {
		t.Fatal(err)
	}
	w := watcher.Disabled(root)
	t.Cleanup(w.Close)
	server := httptest.NewServer(New(v, w, nil, cfg).Router())
	t.Cleanup(server.Close)

	f := &crossSiteFixture{t: t, server: server, root: root}
	jar := loginAndJar(t, server, cfg.AuthToken)
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/", nil)
	for _, c := range jar.Cookies(req.URL) {
		if c.Name == sessionCookieName {
			f.cookie = c.Name + "=" + c.Value
		}
	}
	if f.cookie == "" {
		t.Fatal("login set no session cookie")
	}
	return f
}

// selfOrigin is the origin the web client is served from.
func (f *crossSiteFixture) selfOrigin() string { return f.server.URL }

func (f *crossSiteFixture) do(method, path, contentType string, body []byte, headers map[string]string) *http.Response {
	f.t.Helper()
	req, err := http.NewRequest(method, f.server.URL+path, bytes.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	if body == nil {
		req.Body = http.NoBody
		req.ContentLength = 0
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (f *crossSiteFixture) keepNote() string {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.root, "inbox", "Keep.md"))
	if err != nil {
		if os.IsNotExist(err) {
			return "<deleted>"
		}
		f.t.Fatal(err)
	}
	return string(raw)
}

func jsonBody(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The binds the server ships with: the binary's default and the Docker image's.
var shippedBinds = []string{"127.0.0.1:7878", "0.0.0.0:7878"}

// On the default loopback bind, a page on another port of the same machine is
// another origin. It gets no CORS headers, so it cannot read an answer, though
// SameSite (which ignores ports) lets the cookie ride along.
func TestLoopbackBindGivesOtherLoopbackPortsNoCredentialedCORS(t *testing.T) {
	f := newCrossSiteFixture(t, config.Config{Bind: "127.0.0.1:7878"})
	for _, method := range []string{http.MethodGet, http.MethodOptions} {
		resp := f.do(method, "/api/notes", "", nil, map[string]string{
			"Origin": "http://127.0.0.1:9999",
			"Cookie": f.cookie,
		})
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("%s: Allow-Origin = %q, want none", method, got)
		}
		if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "" {
			t.Errorf("%s: Allow-Credentials = %q, want none", method, got)
		}
	}
}

// Writes from that page are refused outright, cookie or not, whether the
// browser reports Sec-Fetch-Site or only Origin.
func TestLoopbackPortPageCannotChangeTheVault(t *testing.T) {
	f := newCrossSiteFixture(t, config.Config{Bind: "127.0.0.1:7878"})
	for _, fetchSite := range []string{"same-site", ""} {
		headers := map[string]string{"Origin": "http://127.0.0.1:9999", "Cookie": f.cookie}
		if fetchSite != "" {
			headers["Sec-Fetch-Site"] = fetchSite
		}
		write := f.do(http.MethodPost, "/api/notes/write", "application/json",
			jsonBody(t, map[string]string{"path": "inbox/Keep.md", "body": "overwritten"}), headers)
		if write.StatusCode != http.StatusForbidden {
			t.Errorf("Sec-Fetch-Site %q: write status = %d, want 403", fetchSite, write.StatusCode)
		}
		del := f.do(http.MethodPost, "/api/notes/delete", "application/json",
			jsonBody(t, map[string]string{"path": "inbox/Keep.md"}), headers)
		if del.StatusCode != http.StatusForbidden {
			t.Errorf("Sec-Fetch-Site %q: delete status = %d, want 403", fetchSite, del.StatusCode)
		}
		empty := f.do(http.MethodPost, "/api/notes/empty-trash", "", nil, headers)
		if empty.StatusCode != http.StatusForbidden {
			t.Errorf("Sec-Fetch-Site %q: empty-trash status = %d, want 403", fetchSite, empty.StatusCode)
		}
	}
	if got := f.keepNote(); got != "# Keep" {
		t.Fatalf("note changed to %q", got)
	}
}

// A text/plain body can hold valid JSON. On both shipped binds such a POST from
// another page is refused by the cross-site guard, and one with no browser
// headers by the body not being labelled JSON.
func TestTextPlainFormPostCannotWriteOrDelete(t *testing.T) {
	// A text/plain form sends name=value, which can add up to a JSON document.
	formBody := func(v map[string]string) []byte { return jsonBody(t, v) }
	for _, bind := range shippedBinds {
		f := newCrossSiteFixture(t, config.Config{Bind: bind})
		for _, origin := range []string{"https://evil.example", "http://127.0.0.1:9999"} {
			headers := map[string]string{"Origin": origin, "Sec-Fetch-Site": "cross-site", "Cookie": f.cookie}
			write := f.do(http.MethodPost, "/api/notes/write", "text/plain",
				formBody(map[string]string{"path": "inbox/Keep.md", "body": "overwritten"}), headers)
			if write.StatusCode != http.StatusForbidden {
				t.Errorf("%s from %s: text/plain write status = %d, want 403", bind, origin, write.StatusCode)
			}
			del := f.do(http.MethodPost, "/api/notes/delete", "text/plain",
				formBody(map[string]string{"path": "inbox/Keep.md"}), headers)
			if del.StatusCode != http.StatusForbidden {
				t.Errorf("%s from %s: text/plain delete status = %d, want 403", bind, origin, del.StatusCode)
			}
		}
		// The JSON label is required even with no browser headers at all.
		for _, contentType := range []string{"text/plain", "application/x-www-form-urlencoded", ""} {
			headers := map[string]string{"Cookie": f.cookie}
			write := f.do(http.MethodPost, "/api/notes/write", contentType,
				formBody(map[string]string{"path": "inbox/Keep.md", "body": "overwritten"}), headers)
			if write.StatusCode != http.StatusUnsupportedMediaType {
				t.Errorf("%s: %q write status = %d, want 415", bind, contentType, write.StatusCode)
			}
			del := f.do(http.MethodPost, "/api/notes/delete", contentType,
				formBody(map[string]string{"path": "inbox/Keep.md"}), headers)
			if del.StatusCode != http.StatusUnsupportedMediaType {
				t.Errorf("%s: %q delete status = %d, want 415", bind, contentType, del.StatusCode)
			}
		}
		if got := f.keepNote(); got != "# Keep" {
			t.Fatalf("%s: note changed to %q", bind, got)
		}
	}
}

func dialWatch(t *testing.T, f *crossSiteFixture, headers http.Header) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	url := "ws" + strings.TrimPrefix(f.server.URL, "http") + "/api/watch"
	conn, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: headers})
	if conn != nil {
		t.Cleanup(func() { conn.Close(websocket.StatusNormalClosure, "") })
	}
	return conn, resp, err
}

// A WebSocket handshake is never subject to CORS, so the watch feed checks the
// page itself.
func TestWatchSocketRefusesForeignOrigins(t *testing.T) {
	for _, bind := range shippedBinds {
		for _, tc := range []struct{ origin, site string }{
			{"http://127.0.0.1:9999", "same-site"},
			{"http://127.0.0.1:9999", ""},
			{"https://evil.example", "cross-site"},
		} {
			// A fresh server each time: refused handshakes from one address
			// back off, and the next would answer 429.
			f := newCrossSiteFixture(t, config.Config{Bind: bind})
			headers := http.Header{"Origin": {tc.origin}, "Cookie": {f.cookie}}
			if tc.site != "" {
				headers.Set("Sec-Fetch-Site", tc.site)
			}
			_, resp, err := dialWatch(t, f, headers)
			if err == nil {
				t.Errorf("%s: socket from %s (%q) opened", bind, tc.origin, tc.site)
				continue
			}
			if resp == nil || resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s: socket from %s (%q): %v, want 403", bind, tc.origin, tc.site, err)
			}
		}
	}
}

// The web client, on its own origin with its cookie, keeps every call it makes:
// JSON writes, the multipart upload, the bodyless POSTs that send no
// Content-Type at all, and the watch socket.
func TestWebClientOnItsOwnOriginKeepsWorking(t *testing.T) {
	for _, bind := range shippedBinds {
		f := newCrossSiteFixture(t, config.Config{Bind: bind})
		// A browser with Fetch Metadata, and an older one with Origin alone.
		for _, site := range []string{"same-origin", ""} {
			headers := map[string]string{"Origin": f.selfOrigin(), "Cookie": f.cookie}
			if site != "" {
				headers["Sec-Fetch-Site"] = site
			}
			list := f.do(http.MethodGet, "/api/notes", "", nil, headers)
			if list.StatusCode != http.StatusOK {
				t.Errorf("%s %q: list status = %d", bind, site, list.StatusCode)
			}
			write := f.do(http.MethodPost, "/api/notes/write", "application/json",
				jsonBody(t, map[string]string{"path": "inbox/Web.md", "body": "from the web client"}), headers)
			if write.StatusCode != http.StatusOK {
				t.Errorf("%s %q: write status = %d", bind, site, write.StatusCode)
			}
			charset := f.do(http.MethodPost, "/api/notes/write", "application/json; charset=utf-8",
				jsonBody(t, map[string]string{"path": "inbox/Web.md", "body": "charset"}), headers)
			if charset.StatusCode != http.StatusOK {
				t.Errorf("%s %q: write with charset status = %d", bind, site, charset.StatusCode)
			}
			var form bytes.Buffer
			mw := multipart.NewWriter(&form)
			_ = mw.WriteField("notePath", "inbox/Web.md")
			part, _ := mw.CreateFormFile("file", "pic.png")
			_, _ = part.Write([]byte("png bytes"))
			_ = mw.Close()
			upload := f.do(http.MethodPost, "/api/assets/upload", mw.FormDataContentType(), form.Bytes(), headers)
			if upload.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(upload.Body)
				t.Errorf("%s %q: upload status = %d: %s", bind, site, upload.StatusCode, body)
			}
			for _, path := range []string{"/api/notes/empty-trash", "/api/assets/empty-deleted", "/api/demo/remove"} {
				resp := f.do(http.MethodPost, path, "", nil, headers)
				if resp.StatusCode/100 != 2 {
					t.Errorf("%s %q: bodyless %s status = %d", bind, site, path, resp.StatusCode)
				}
			}
			socketHeaders := http.Header{"Origin": {f.selfOrigin()}, "Cookie": {f.cookie}}
			if site != "" {
				socketHeaders.Set("Sec-Fetch-Site", site)
			}
			if _, resp, err := dialWatch(t, f, socketHeaders); err != nil {
				t.Errorf("%s %q: watch socket: %v (%v)", bind, site, err, resp)
			}
		}
		// Logging out is bodyless too, and last, since it ends the session.
		logout := f.do(http.MethodPost, "/api/session/logout", "", nil, map[string]string{
			"Origin": f.selfOrigin(), "Sec-Fetch-Site": "same-origin", "Cookie": f.cookie,
		})
		if logout.StatusCode != http.StatusOK {
			t.Errorf("%s: logout status = %d", bind, logout.StatusCode)
		}
	}
}

// Behind a proxy that rewrites Host, the browser's own word that a request is
// same-origin still lets the web client write.
func TestSameOriginFetchMetadataTrustedOverHostMismatch(t *testing.T) {
	f := newCrossSiteFixture(t, config.Config{Bind: "127.0.0.1:7878"})
	resp := f.do(http.MethodPost, "/api/notes/write", "application/json",
		jsonBody(t, map[string]string{"path": "inbox/Proxy.md", "body": "x"}),
		map[string]string{"Origin": "https://notes.example.com", "Sec-Fetch-Site": "same-origin", "Cookie": f.cookie})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

// Clients outside a browser send no Origin and no Sec-Fetch-Site: the desktop
// app's main process (whose Node fetch does send Sec-Fetch-Mode), the zn CLI,
// the phone apps' native HTTP, MCP clients, curl.
func TestBearerClientsWithoutOriginKeepWorking(t *testing.T) {
	for _, bind := range shippedBinds {
		f := newCrossSiteFixture(t, config.Config{Bind: bind})
		for _, extra := range []map[string]string{{}, {"Sec-Fetch-Mode": "cors"}} {
			headers := map[string]string{"Authorization": "Bearer " + crossSiteToken}
			for k, v := range extra {
				headers[k] = v
			}
			write := f.do(http.MethodPost, "/api/notes/write", "application/json",
				jsonBody(t, map[string]string{"path": "inbox/Cli.md", "body": "from zn"}), headers)
			if write.StatusCode != http.StatusOK {
				t.Errorf("%s %v: bearer write status = %d", bind, extra, write.StatusCode)
			}
			empty := f.do(http.MethodPost, "/api/notes/empty-trash", "", nil, headers)
			if empty.StatusCode != http.StatusNoContent {
				t.Errorf("%s %v: bearer empty-trash status = %d", bind, extra, empty.StatusCode)
			}
			// Android's HttpURLConnection may label a bodyless POST as a form.
			empty = f.do(http.MethodPost, "/api/notes/empty-trash", "application/x-www-form-urlencoded", nil, headers)
			if empty.StatusCode != http.StatusNoContent {
				t.Errorf("%s %v: bearer empty-trash labelled as a form status = %d", bind, extra, empty.StatusCode)
			}
		}
		if _, resp, err := dialWatch(t, f, http.Header{"Authorization": {"Bearer " + crossSiteToken}}); err != nil {
			t.Errorf("%s: bearer watch socket: %v (%v)", bind, err, resp)
		}
	}
}

// What the README documents for browser clients on other origins still holds.
func TestAllowedOriginsAndDevModeStillAdmitted(t *testing.T) {
	write := func(f *crossSiteFixture, headers map[string]string) *http.Response {
		return f.do(http.MethodPost, "/api/notes/write", "application/json",
			jsonBody(t, map[string]string{"path": "inbox/Other.md", "body": "x"}), headers)
	}

	t.Run("listed origin keeps the session", func(t *testing.T) {
		f := newCrossSiteFixture(t, config.Config{Bind: "0.0.0.0:7878", AllowedOrigins: []string{"https://app.example.com"}})
		resp := write(f, map[string]string{"Origin": "https://app.example.com", "Sec-Fetch-Site": "cross-site", "Cookie": f.cookie})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "true" {
			t.Errorf("Allow-Credentials = %q", got)
		}
		_, socket, err := dialWatch(t, f, http.Header{"Origin": {"https://app.example.com"}, "Sec-Fetch-Site": {"cross-site"}, "Cookie": {f.cookie}})
		if err != nil {
			t.Errorf("listed origin's watch socket: %v (%v)", err, socket)
		}
	})

	t.Run("listed loopback port on a loopback bind", func(t *testing.T) {
		f := newCrossSiteFixture(t, config.Config{Bind: "127.0.0.1:7878", AllowedOrigins: []string{"http://127.0.0.1:9999"}})
		resp := write(f, map[string]string{"Origin": "http://127.0.0.1:9999", "Sec-Fetch-Site": "same-site", "Cookie": f.cookie})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})

	t.Run("dev mode admits a Vite dev server", func(t *testing.T) {
		for _, bind := range shippedBinds {
			f := newCrossSiteFixture(t, config.Config{Bind: bind, DevMode: true})
			resp := write(f, map[string]string{"Origin": "http://localhost:5173", "Sec-Fetch-Site": "same-site", "Cookie": f.cookie})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s: status = %d", bind, resp.StatusCode)
			}
			if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
				t.Errorf("%s: Allow-Origin = %q", bind, got)
			}
		}
	})

	t.Run("wildcard admits bearer clients but never the session", func(t *testing.T) {
		f := newCrossSiteFixture(t, config.Config{Bind: "0.0.0.0:7878", AllowedOrigins: []string{"*"}})
		bearer := write(f, map[string]string{"Origin": "https://tool.example", "Sec-Fetch-Site": "cross-site", "Authorization": "Bearer " + crossSiteToken})
		if bearer.StatusCode != http.StatusOK {
			t.Errorf("bearer status = %d", bearer.StatusCode)
		}
		cookie := write(f, map[string]string{"Origin": "https://tool.example", "Sec-Fetch-Site": "cross-site", "Cookie": f.cookie})
		if cookie.StatusCode != http.StatusUnauthorized {
			t.Errorf("cookie-only status = %d, want 401", cookie.StatusCode)
		}
		if _, _, err := dialWatch(t, f, http.Header{"Origin": {"https://tool.example"}, "Sec-Fetch-Site": {"cross-site"}, "Cookie": {f.cookie}}); err == nil {
			t.Error("wildcard origin opened the watch socket on the session cookie")
		}
	})

	t.Run("unlisted origin in dev mode on a public host", func(t *testing.T) {
		f := newCrossSiteFixture(t, config.Config{Bind: "0.0.0.0:7878", DevMode: true})
		resp := write(f, map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site", "Cookie": f.cookie})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", resp.StatusCode)
		}
	})
}

// A browser that says a request is cross-site but sends no Origin is not one
// this server can place, so it is refused.
func TestCrossSiteWithoutOriginRefused(t *testing.T) {
	f := newCrossSiteFixture(t, config.Config{Bind: "127.0.0.1:7878"})
	resp := f.do(http.MethodPost, "/api/notes/empty-trash", "", nil, map[string]string{"Sec-Fetch-Site": "cross-site", "Cookie": f.cookie})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

// Opaque origins (a sandboxed frame, a data: or file: page) send Origin: null.
// It is refused like any other unlisted origin, and admitted once listed.
func TestNullOriginRefusedUnlessListed(t *testing.T) {
	note := jsonBody(t, map[string]string{"path": "inbox/Null.md", "body": "x"})
	f := newCrossSiteFixture(t, config.Config{Bind: "127.0.0.1:7878"})
	for _, site := range []string{"cross-site", ""} {
		headers := map[string]string{"Origin": "null", "Cookie": f.cookie}
		if site != "" {
			headers["Sec-Fetch-Site"] = site
		}
		if resp := f.do(http.MethodPost, "/api/notes/write", "application/json", note, headers); resp.StatusCode != http.StatusForbidden {
			t.Errorf("null origin (%q): status = %d, want 403", site, resp.StatusCode)
		}
	}
	listed := newCrossSiteFixture(t, config.Config{Bind: "127.0.0.1:7878", AllowedOrigins: []string{"null"}})
	resp := listed.do(http.MethodPost, "/api/notes/write", "application/json", note,
		map[string]string{"Origin": "null", "Sec-Fetch-Site": "cross-site", "Cookie": listed.cookie})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("listed null origin: status = %d, want 200", resp.StatusCode)
	}
}

// Sec-Fetch-Site: none is a request the person started, which no page can
// send. A value no browser sends today is treated as cross-site: Origin decides.
func TestSecFetchSiteNoneAndUnknownValues(t *testing.T) {
	f := newCrossSiteFixture(t, config.Config{Bind: "127.0.0.1:7878"})
	note := jsonBody(t, map[string]string{"path": "inbox/Site.md", "body": "x"})
	if resp := f.do(http.MethodPost, "/api/notes/write", "application/json", note,
		map[string]string{"Sec-Fetch-Site": "none", "Cookie": f.cookie}); resp.StatusCode != http.StatusOK {
		t.Errorf("none: status = %d, want 200", resp.StatusCode)
	}
	for _, tc := range []struct {
		origin string
		want   int
	}{
		{"https://evil.example", http.StatusForbidden},
		{"", http.StatusForbidden},
		{f.selfOrigin(), http.StatusOK},
	} {
		headers := map[string]string{"Sec-Fetch-Site": "cross-galaxy", "Cookie": f.cookie}
		if tc.origin != "" {
			headers["Origin"] = tc.origin
		}
		if resp := f.do(http.MethodPost, "/api/notes/write", "application/json", note, headers); resp.StatusCode != tc.want {
			t.Errorf("unknown value, origin %q: status = %d, want %d", tc.origin, resp.StatusCode, tc.want)
		}
	}
}

// A multipart form and a bodyless form POST are both things a page on another
// site can send without a preflight.
func TestCrossSiteUploadAndLogoutRefused(t *testing.T) {
	f := newCrossSiteFixture(t, config.Config{Bind: "127.0.0.1:7878"})
	headers := map[string]string{"Origin": "http://127.0.0.1:9999", "Sec-Fetch-Site": "same-site", "Cookie": f.cookie}
	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	_ = mw.WriteField("notePath", "")
	part, _ := mw.CreateFormFile("file", "planted.png")
	_, _ = part.Write([]byte("png bytes"))
	_ = mw.Close()
	if resp := f.do(http.MethodPost, "/api/assets/upload", mw.FormDataContentType(), form.Bytes(), headers); resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site upload: status = %d, want 403", resp.StatusCode)
	}
	if matches, _ := filepath.Glob(filepath.Join(f.root, "*", "planted*")); len(matches) > 0 {
		t.Errorf("cross-site upload wrote %v", matches)
	}
	if resp := f.do(http.MethodPost, "/api/session/logout", "", nil, headers); resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site logout: status = %d, want 403", resp.StatusCode)
	}
	// The session survives.
	if resp := f.do(http.MethodGet, "/api/vault", "", nil, map[string]string{"Cookie": f.cookie}); resp.StatusCode != http.StatusOK {
		t.Errorf("session after refused logout: status = %d", resp.StatusCode)
	}
}

// Every method but GET, HEAD and OPTIONS goes through the guard, whether or not
// a route exists for it, /mcp included.
func TestCrossSiteGuardCoversEveryUnsafeMethod(t *testing.T) {
	f := newCrossSiteFixture(t, config.Config{Bind: "127.0.0.1:7878"})
	headers := map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site", "Cookie": f.cookie}
	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
		resp := f.do(method, "/api/notes/write", "application/json",
			jsonBody(t, map[string]string{"path": "inbox/Keep.md", "body": "x"}), headers)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", method, resp.StatusCode)
		}
	}
	if got := f.keepNote(); got != "# Keep" {
		t.Fatalf("note changed to %q", got)
	}

	mcp, _ := newMCPServer(t, func(c *config.Config) { c.Bind = "127.0.0.1:7878" }, mcpConfig("write"))
	for _, method := range []string{http.MethodDelete, http.MethodPost} {
		req, _ := http.NewRequest(method, mcp.URL+"/mcp", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+mcpTestToken)
		req.Header.Set("Origin", "https://evil.example")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s /mcp from another site: status = %d, want 403", method, resp.StatusCode)
		}
	}
}

// The legacy root-level /watch is the same feed and gets the same check.
func TestLegacyWatchRouteChecksOriginToo(t *testing.T) {
	f := newCrossSiteFixture(t, config.Config{Bind: "127.0.0.1:7878"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(f.server.URL, "http") + "/watch"
	_, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{
		"Origin": {"http://127.0.0.1:9999"}, "Sec-Fetch-Site": {"same-site"}, "Cookie": {f.cookie},
	}})
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("legacy /watch from another port: %v (%v), want 403", err, resp)
	}
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{
		"Origin": {f.selfOrigin()}, "Sec-Fetch-Site": {"same-origin"}, "Cookie": {f.cookie},
	}})
	if err != nil {
		t.Fatalf("legacy /watch from the web client: %v", err)
	}
	conn.Close(websocket.StatusNormalClosure, "")
}

// The watch handler checks the page on its own as well, so the feed stays
// closed even where the outer guard is not in front of it.
func TestWatchHandlerRefusesForeignOriginWithoutTheGuard(t *testing.T) {
	root := t.TempDir()
	v, err := vault.New(root, vault.Options{})
	if err != nil {
		t.Fatal(err)
	}
	w := watcher.Disabled(root)
	t.Cleanup(w.Close)
	s := New(v, w, nil, config.Config{VaultPath: root, Bind: "127.0.0.1:7878"})
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7878/api/watch", nil)
	req.Header.Set("Origin", "http://127.0.0.1:9999")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	req.Header.Set("Upgrade", "websocket")
	rec := httptest.NewRecorder()
	s.watchWS(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

// With both the wildcard and dev mode on, a loopback dev origin keeps its
// session, as the banner says, and any other origin still gets none.
func TestWildcardWithDevModeKeepsTheDevServersSession(t *testing.T) {
	f := newCrossSiteFixture(t, config.Config{Bind: "127.0.0.1:7878", AllowedOrigins: []string{"*"}, DevMode: true})
	note := jsonBody(t, map[string]string{"path": "inbox/Dev.md", "body": "x"})
	dev := f.do(http.MethodPost, "/api/notes/write", "application/json", note,
		map[string]string{"Origin": "http://localhost:5173", "Sec-Fetch-Site": "same-site", "Cookie": f.cookie})
	if dev.StatusCode != http.StatusOK {
		t.Errorf("dev origin: status = %d, want 200", dev.StatusCode)
	}
	if got := dev.Header.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("dev origin: Allow-Credentials = %q, want true", got)
	}
	other := f.do(http.MethodPost, "/api/notes/write", "application/json", note,
		map[string]string{"Origin": "https://tool.example", "Sec-Fetch-Site": "cross-site", "Cookie": f.cookie})
	if other.StatusCode != http.StatusUnauthorized {
		t.Errorf("wildcard origin with only the cookie: status = %d, want 401", other.StatusCode)
	}
	if got := other.Header.Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("wildcard origin: Allow-Credentials = %q, want none", got)
	}
}
