package httpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZenNotes/zennotes/apps/server/internal/config"
	"github.com/ZenNotes/zennotes/apps/server/internal/vault"
)

func newTestServer(t *testing.T, cfg config.Config) (*httptest.Server, *vault.Vault) {
	t.Helper()

	v, err := vault.New(cfg.VaultPath, vault.Options{
		FileMode:      cfg.VaultFileMode,
		DirMode:       cfg.VaultDirMode,
		MaxAssetBytes: cfg.MaxAssetBytes,
	})
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}

	server := httptest.NewServer(New(v, nil, nil, cfg).Router())
	t.Cleanup(server.Close)
	return server, v
}

// loginAndJar logs in with the given token and returns a cookiejar
// that subsequent calls can reuse.
func loginAndJar(t *testing.T, server *httptest.Server, token string) http.CookieJar {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	client := &http.Client{Jar: jar}
	body, _ := json.Marshal(map[string]string{"token": token})
	resp, err := client.Post(server.URL+"/api/session/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status: %d", resp.StatusCode)
	}
	return jar
}

func TestSessionLoginProtectsVaultRoutes(t *testing.T) {
	root := t.TempDir()
	server, v := newTestServer(t, config.Config{
		VaultPath:        root,
		DefaultVaultPath: root,
		Bind:             "127.0.0.1:7878",
		AuthToken:        "secret-token",
		BrowseRoots:      []string{root},
	})

	unauthenticatedResp, err := http.Get(server.URL + "/api/vault")
	if err != nil {
		t.Fatalf("GET /api/vault without auth: %v", err)
	}
	defer unauthenticatedResp.Body.Close()
	if unauthenticatedResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", unauthenticatedResp.StatusCode)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	client := &http.Client{Jar: jar}

	loginBody, err := json.Marshal(map[string]string{"token": "secret-token"})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	loginResp, err := client.Post(server.URL+"/api/session/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatalf("POST /api/session/login: %v", err)
	}
	defer loginResp.Body.Close()
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from login, got %d", loginResp.StatusCode)
	}

	loginURL, err := url.Parse(server.URL + "/api/session/login")
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	if len(jar.Cookies(loginURL)) == 0 {
		t.Fatal("expected login to set a session cookie")
	}

	authedResp, err := client.Get(server.URL + "/api/vault")
	if err != nil {
		t.Fatalf("GET /api/vault with session cookie: %v", err)
	}
	defer authedResp.Body.Close()
	if authedResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with session cookie, got %d", authedResp.StatusCode)
	}

	var info struct {
		Root string `json:"root"`
	}
	if err := json.NewDecoder(authedResp.Body).Decode(&info); err != nil {
		t.Fatalf("decode /api/vault response: %v", err)
	}
	if info.Root != v.Root() {
		t.Fatalf("expected vault root %q, got %q", v.Root(), info.Root)
	}
}

func TestBrowseRootsEnforced(t *testing.T) {
	parent := t.TempDir()
	allowedRoot := filepath.Join(parent, "allowed")
	blockedRoot := filepath.Join(parent, "blocked")
	if err := os.MkdirAll(allowedRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll allowedRoot: %v", err)
	}
	if err := os.MkdirAll(blockedRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll blockedRoot: %v", err)
	}

	server, _ := newTestServer(t, config.Config{
		VaultPath:        allowedRoot,
		DefaultVaultPath: allowedRoot,
		Bind:             "127.0.0.1:7878",
		AuthToken:        "secret-token",
		BrowseRoots:      []string{allowedRoot},
	})

	request, err := http.NewRequest(http.MethodGet, server.URL+"/api/fs/browse?path="+url.QueryEscape(blockedRoot), nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	request.Header.Set("Authorization", "Bearer secret-token")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET /api/fs/browse outside allowed root: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for blocked browse root, got %d", response.StatusCode)
	}
}

// --- T1.2 upload size limit ---

func TestUploadAssetRespects413(t *testing.T) {
	root := t.TempDir()
	server, _ := newTestServer(t, config.Config{
		VaultPath:     root,
		Bind:          "127.0.0.1:7878",
		AuthToken:     "secret-token",
		MaxAssetBytes: 64,
		MaxNoteBytes:  64,
	})
	jar := loginAndJar(t, server, "secret-token")
	client := &http.Client{Jar: jar}

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	_ = mw.WriteField("notePath", "note.md")
	part, _ := mw.CreateFormFile("file", "x.bin")
	_, _ = part.Write(bytes.Repeat([]byte("a"), 1024))
	_ = mw.Close()

	resp, err := client.Post(server.URL+"/api/assets/upload", mw.FormDataContentType(), body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge && resp.StatusCode != http.StatusBadRequest {
		// http.MaxBytesReader returns 400 before ParseMultipartForm runs
		// for over-cap requests; ImportAsset's own ErrAssetTooLarge maps
		// to 413 if the multipart parser somehow lets it through. Either
		// is acceptable here.
		t.Fatalf("expected 4xx large-body rejection, got %d", resp.StatusCode)
	}
}

func TestWriteNoteRespects413(t *testing.T) {
	root := t.TempDir()
	server, _ := newTestServer(t, config.Config{
		VaultPath:    root,
		Bind:         "127.0.0.1:7878",
		AuthToken:    "secret-token",
		MaxNoteBytes: 64,
	})
	jar := loginAndJar(t, server, "secret-token")
	client := &http.Client{Jar: jar}

	huge := strings.Repeat("a", 200000)
	body, _ := json.Marshal(map[string]string{"path": "x.md", "body": huge})
	resp, err := client.Post(server.URL+"/api/notes/write", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 4 {
		t.Fatalf("expected 4xx for oversized note, got %d", resp.StatusCode)
	}
}

// --- T2.4 trusted-proxies gate ---

func TestForwardedProtoIgnoredWithoutTrust(t *testing.T) {
	server, _ := newTestServer(t, config.Config{
		VaultPath: t.TempDir(),
		Bind:      "127.0.0.1:7878",
		AuthToken: "secret-token",
	})
	body, _ := json.Marshal(map[string]string{"token": "secret-token"})
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/session/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "zennotes_session" && c.Secure {
			t.Fatalf("Secure cookie set despite untrusted X-Forwarded-Proto")
		}
	}
}

func TestForwardedProtoHonouredWhenTrusted(t *testing.T) {
	_, loop, _ := net.ParseCIDR("127.0.0.0/8")
	server, _ := newTestServer(t, config.Config{
		VaultPath:      t.TempDir(),
		Bind:           "127.0.0.1:7878",
		AuthToken:      "secret-token",
		TrustedProxies: []net.IPNet{*loop},
	})
	body, _ := json.Marshal(map[string]string{"token": "secret-token"})
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/session/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	var found bool
	for _, c := range resp.Cookies() {
		if c.Name == "zennotes_session" && c.Secure {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected Secure cookie when peer is trusted and X-Forwarded-Proto=https")
	}
}

// --- T2.5 HSTS ---

func TestHSTSOnlyWhenEffectiveHTTPS(t *testing.T) {
	root := t.TempDir()

	// Without BehindTLS or trusted proxies: no HSTS.
	plain, _ := newTestServer(t, config.Config{VaultPath: root, Bind: "127.0.0.1:7878"})
	resp, err := http.Get(plain.URL + "/api/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("HSTS unexpectedly sent on plain HTTP: %q", got)
	}

	// With BehindTLS=true: HSTS sent.
	tls, _ := newTestServer(t, config.Config{VaultPath: t.TempDir(), Bind: "127.0.0.1:7878", BehindTLS: true})
	resp, err = http.Get(tls.URL + "/api/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("Strict-Transport-Security"); !strings.Contains(got, "max-age=") {
		t.Fatalf("expected HSTS header with BehindTLS=1, got %q", got)
	}
}

// --- T3.9 token rotation ---

func TestRotateTokenFullFlow(t *testing.T) {
	server, _ := newTestServer(t, config.Config{
		VaultPath: t.TempDir(),
		Bind:      "127.0.0.1:7878",
		AuthToken: "current-token-xxxxxxxx",
		// SaveHost would otherwise touch the user config file. Redirect:
	})
	t.Setenv("ZENNOTES_CONFIG_PATH", filepath.Join(t.TempDir(), "host.json"))

	jar := loginAndJar(t, server, "current-token-xxxxxxxx")
	client := &http.Client{Jar: jar}

	// Rotate.
	body, _ := json.Marshal(map[string]string{
		"currentToken": "current-token-xxxxxxxx",
		"newToken":     "rotated-token-yyyyyyy",
	})
	resp, err := client.Post(server.URL+"/api/session/rotate-token", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rotate: expected 200, got %d", resp.StatusCode)
	}

	// Old session is now invalidated.
	resp, err = client.Get(server.URL + "/api/vault")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old session should be invalidated, got %d", resp.StatusCode)
	}

	// New token logs in (do this first so the success resets the rate
	// limiter; otherwise the next failed attempt would trip the
	// inter-attempt backoff and the assertion below would 429).
	loginNew, _ := json.Marshal(map[string]string{"token": "rotated-token-yyyyyyy"})
	resp, err = http.Post(server.URL+"/api/session/login", "application/json", bytes.NewReader(loginNew))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("new token should log in, got %d", resp.StatusCode)
	}

	// Old token can no longer log in.
	loginOld, _ := json.Marshal(map[string]string{"token": "current-token-xxxxxxxx"})
	resp, err = http.Post(server.URL+"/api/session/login", "application/json", bytes.NewReader(loginOld))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("old token should be rejected after rotation, got %d", resp.StatusCode)
	}
}

func TestRotateTokenRejectsShortToken(t *testing.T) {
	server, _ := newTestServer(t, config.Config{
		VaultPath: t.TempDir(),
		Bind:      "127.0.0.1:7878",
		AuthToken: "current-token-xxxxxxxx",
	})
	t.Setenv("ZENNOTES_CONFIG_PATH", filepath.Join(t.TempDir(), "host.json"))
	jar := loginAndJar(t, server, "current-token-xxxxxxxx")
	client := &http.Client{Jar: jar}

	body, _ := json.Marshal(map[string]string{
		"currentToken": "current-token-xxxxxxxx",
		"newToken":     "tooshort",
	})
	resp, err := client.Post(server.URL+"/api/session/rotate-token", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for short new token, got %d", resp.StatusCode)
	}
}

func TestRotateTokenRejectsWrongCurrent(t *testing.T) {
	server, _ := newTestServer(t, config.Config{
		VaultPath: t.TempDir(),
		Bind:      "127.0.0.1:7878",
		AuthToken: "current-token-xxxxxxxx",
	})
	t.Setenv("ZENNOTES_CONFIG_PATH", filepath.Join(t.TempDir(), "host.json"))
	jar := loginAndJar(t, server, "current-token-xxxxxxxx")
	client := &http.Client{Jar: jar}

	body, _ := json.Marshal(map[string]string{
		"currentToken": "wrong-token",
		"newToken":     "rotated-token-yyyyyyy",
	})
	resp, err := client.Post(server.URL+"/api/session/rotate-token", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong current, got %d", resp.StatusCode)
	}
}

func TestRotateTokenRejectsExternalTokenSource(t *testing.T) {
	server, _ := newTestServer(t, config.Config{
		VaultPath:       t.TempDir(),
		Bind:            "127.0.0.1:7878",
		AuthToken:       "current-token-xxxxxxxx",
		AuthTokenSource: config.AuthTokenSourceEnv,
	})
	t.Setenv("ZENNOTES_CONFIG_PATH", filepath.Join(t.TempDir(), "host.json"))
	jar := loginAndJar(t, server, "current-token-xxxxxxxx")
	client := &http.Client{Jar: jar}

	body, _ := json.Marshal(map[string]string{
		"currentToken": "current-token-xxxxxxxx",
		"newToken":     "rotated-token-yyyyyyy",
	})
	resp, err := client.Post(server.URL+"/api/session/rotate-token", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 for externally managed token, got %d", resp.StatusCode)
	}
}

func TestWriteErrorDoesNotExposeInternalDetails(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, errors.New("open /Users/example/private/vault/secret.md: permission denied"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "/Users/example") || strings.Contains(body, "permission denied") {
		t.Fatalf("internal error leaked details: %q", body)
	}
	if !strings.Contains(body, "internal server error") {
		t.Fatalf("expected generic error body, got %q", body)
	}
}

func TestIsLoopbackBindTreatsEmptyHostAsNonLoopback(t *testing.T) {
	cases := []struct {
		bind string
		want bool
	}{
		{":7878", false},
		{"0.0.0.0:7878", false},
		{"[::]:7878", false},
		{"127.0.0.1:7878", true},
		{"[::1]:7878", true},
		{"localhost:7878", true},
	}

	for _, tc := range cases {
		if got := isLoopbackBind(tc.bind); got != tc.want {
			t.Fatalf("isLoopbackBind(%q) = %v, want %v", tc.bind, got, tc.want)
		}
	}
}

// --- T3.10 CORS rejection log ---

func TestCORSRejectionLoggedOncePerOrigin(t *testing.T) {
	var buf strings.Builder
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	server, _ := newTestServer(t, config.Config{
		VaultPath: t.TempDir(),
		Bind:      "127.0.0.1:7878",
	})
	send := func(origin string) {
		req, _ := http.NewRequest(http.MethodGet, server.URL+"/api/healthz", nil)
		req.Header.Set("Origin", origin)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}

	send("https://evil.example.com")
	send("https://evil.example.com")
	send("https://other.example.com")

	out := buf.String()
	first := strings.Count(out, `evil.example.com"`)
	other := strings.Count(out, `other.example.com"`)
	if first != 1 {
		t.Errorf("expected exactly one log for evil.example.com, got %d:\n%s", first, out)
	}
	if other != 1 {
		t.Errorf("expected exactly one log for other.example.com, got %d:\n%s", other, out)
	}
}
