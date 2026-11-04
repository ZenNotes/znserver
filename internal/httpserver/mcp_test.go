package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZenNotes/znserver/internal/config"
	"github.com/ZenNotes/znserver/internal/vault"
	"github.com/go-chi/chi/v5"
)

// The full scope contract runs through /mcp from the tui repository against a built
// server; see .github/workflows/ci.yml. These cover what belongs to this repository.

const (
	mcpTestServerToken = "server-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	mcpTestToken       = "mcp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func mcpConfig(access string, allow ...string) config.MCPConfig {
	return config.MCPConfig{
		Enabled: true,
		Credentials: []config.MCPCredential{{
			Name:        "test",
			TokenSHA256: sha256.Sum256([]byte(mcpTestToken)),
			Access:      access,
			Allow:       allow,
		}},
	}
}

func newMCPServer(t *testing.T, mutate func(*config.Config), m config.MCPConfig) (*httptest.Server, *Server) {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{
		VaultPath:     root,
		AuthToken:     mcpTestServerToken,
		MaxNoteBytes:  10 << 20,
		MaxAssetBytes: 50 << 20,
		VaultFileMode: 0o600,
		VaultDirMode:  0o700,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	t.Setenv("ZENNOTES_CONFIG_PATH", filepath.Join(t.TempDir(), "server.json"))
	v, err := vault.New(cfg.VaultPath, vault.Options{FileMode: cfg.VaultFileMode, DirMode: cfg.VaultDirMode, MaxAssetBytes: cfg.MaxAssetBytes})
	if err != nil {
		t.Fatal(err)
	}
	s := New(v, nil, nil, cfg)
	if err := s.EnableMCP(m); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(s.Router())
	t.Cleanup(server.Close)
	return server, s
}

func mcpRequest(t *testing.T, url, token, message string) (int, string) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(message))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(body)
}

// mcpCall calls a tool and returns its text, and whether it failed.
func mcpCall(t *testing.T, url, tool string, args map[string]any) (string, bool) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	status, body := mcpRequest(t, url, mcpTestToken, string(raw))
	if status != http.StatusOK {
		t.Fatalf("%s: %d %s", tool, status, body)
	}
	var reply struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &reply); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	if len(reply.Result.Content) == 0 {
		t.Fatalf("no content: %s", body)
	}
	return reply.Result.Content[0].Text, reply.Result.IsError
}

const mcpInit = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"raw","version":"0"}}}`

func TestMCPIsNotRoutedUnlessEnabled(t *testing.T) {
	server, _ := newTestServer(t, config.Config{VaultPath: t.TempDir(), AuthToken: mcpTestServerToken})
	for _, token := range []string{"", mcpTestServerToken} {
		status, body := mcpRequest(t, server.URL+"/mcp", token, mcpInit)
		if status != http.StatusNotFound {
			t.Errorf("token %q: %d %s", token, status, body)
		}
	}
	// Not with EnableMCP told it is off, either.
	off, _ := newMCPServer(t, nil, config.MCPConfig{})
	if status, _ := mcpRequest(t, off.URL+"/mcp", mcpTestToken, mcpInit); status != http.StatusNotFound {
		t.Errorf("EnableMCP off: %d", status)
	}
}

func TestEnableMCPRefuses(t *testing.T) {
	cases := map[string]struct {
		token string
		mcp   config.MCPConfig
		want  string
	}{
		"an open REST API":                {"", mcpConfig("read"), "ZENNOTES_AUTH_TOKEN"},
		"the server's token":              {mcpTestToken, mcpConfig("read"), "server's own token"},
		"the server's token, with spaces": {"  " + mcpTestToken + "\n", mcpConfig("read"), "server's own token"},
		"no access":                       {mcpTestServerToken, mcpConfig(""), "access is required"},
		"a bad path":                      {mcpTestServerToken, mcpConfig("read", "../up"), "allow path"},
		"no credentials":                  {mcpTestServerToken, config.MCPConfig{Enabled: true}, "no MCP credential"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			v, err := vault.New(root, vault.Options{})
			if err != nil {
				t.Fatal(err)
			}
			err = New(v, nil, nil, config.Config{VaultPath: root, AuthToken: tc.token}).EnableMCP(tc.mcp)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

// An in-process call must not inherit /mcp's route, or the server routes it back to
// /mcp and the failure looks like a token problem.
func TestMCPCallsReachTheAPI(t *testing.T) {
	server, s := newMCPServer(t, nil, mcpConfig("write"))
	root := s.currentVault().Root()
	url := server.URL + "/mcp"
	text, failed := mcpCall(t, url, "create_note", map[string]any{"folder": "inbox", "title": "Made"})
	if failed {
		t.Fatalf("create_note: %s", text)
	}
	var note struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(text), &note); err != nil {
		t.Fatalf("%v: %s", err, text)
	}
	// The server's own rules applied: its file mode, not zn's.
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(note.Path)))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("a note made over MCP has mode %o", info.Mode().Perm())
	}
	if text, failed := mcpCall(t, url, "list_notes", map[string]any{}); failed || !strings.Contains(text, "Made") {
		t.Errorf("list_notes: %s", text)
	}
}

func TestMCPUnderABasePath(t *testing.T) {
	server, _ := newMCPServer(t, func(c *config.Config) { c.BasePath = "/notes" }, mcpConfig("write"))
	if text, failed := mcpCall(t, server.URL+"/notes/mcp", "create_note", map[string]any{"folder": "inbox", "title": "Made"}); failed {
		t.Errorf("create_note: %s", text)
	}
	if status, _ := mcpRequest(t, server.URL+"/mcp", mcpTestToken, mcpInit); status == http.StatusOK {
		t.Error("/mcp answered outside the base path")
	}
}

func TestMCPTokensDoNotCross(t *testing.T) {
	server, _ := newMCPServer(t, nil, mcpConfig("write"))
	if status, body := mcpRequest(t, server.URL+"/mcp", mcpTestToken, mcpInit); status != http.StatusOK {
		t.Fatalf("MCP token on /mcp: %d %s", status, body)
	}
	if status, _ := mcpRequest(t, server.URL+"/mcp", mcpTestServerToken, mcpInit); status != http.StatusUnauthorized {
		t.Errorf("server token on /mcp: %d", status)
	}
	for _, route := range []string{"/api/notes/write", "/notes/write", "/api/session/rotate-token", "/api/vault/select", "/api/vault/settings"} {
		if status, _ := mcpRequest(t, server.URL+route, mcpTestToken, `{}`); status != http.StatusUnauthorized {
			t.Errorf("MCP token on %s: %d", route, status)
		}
	}
}

func TestRotationRefusesAnMCPToken(t *testing.T) {
	server, s := newMCPServer(t, func(c *config.Config) { c.AuthTokenSource = config.AuthTokenSourceConfig }, mcpConfig("write"))
	rotate := func(next string) int {
		status, _ := mcpRequest(t, server.URL+"/api/session/rotate-token", s.currentConfig().AuthToken, fmt.Sprintf(`{"currentToken":%q,"newToken":%q}`, s.currentConfig().AuthToken, next))
		return status
	}
	if status := rotate(mcpTestToken); status != http.StatusBadRequest {
		t.Fatalf("rotating to the MCP token: %d", status)
	}
	if status := rotate("  " + mcpTestToken + " "); status != http.StatusBadRequest {
		t.Fatalf("rotating to the MCP token with spaces: %d", status)
	}
	next := strings.Repeat("n", 48)
	if status := rotate(next); status != http.StatusOK {
		t.Fatalf("ordinary rotation: %d", status)
	}
	// MCP keeps working: the calls it makes use the new token.
	if text, failed := mcpCall(t, server.URL+"/mcp", "list_notes", map[string]any{}); failed {
		t.Errorf("list_notes after rotation: %s", text)
	}
}

// ZENNOTES_VAULT_PATH is required with MCP on, and it stops a switch.
func TestPinnedVaultCannotBeSwitched(t *testing.T) {
	second := t.TempDir()
	server, s := newMCPServer(t, nil, mcpConfig("write"))
	t.Setenv("ZENNOTES_VAULT_PATH", s.currentVault().Root())
	status, body := mcpRequest(t, server.URL+"/api/vault/select", mcpTestServerToken, fmt.Sprintf(`{"path":%q}`, second))
	if status != http.StatusConflict {
		t.Errorf("switch: %d %s", status, body)
	}
}

// No tool calls these; a bug in one must still not reach them.
func TestMCPTransportRefusesEverythingElse(t *testing.T) {
	_, s := newMCPServer(t, nil, mcpConfig("write"))
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/session/rotate-token"},
		{http.MethodPost, "/api/session/logout"},
		{http.MethodPost, "/session/rotate-token"},
		{http.MethodPost, "/api/vault/select"},
		{http.MethodPost, "/vault/select"},
		{http.MethodPost, "/api/vault/settings"},
		{http.MethodGet, "/api/fs/browse"},
		{http.MethodGet, "/api/watch"},
		{http.MethodPost, "/api/demo/generate"},
		{http.MethodPost, "/api/workflows/apply"},
		{http.MethodPost, "/api/assets/purge"},
		{http.MethodPost, "/api/assets/empty-deleted"},
		{http.MethodPost, "/api/templates/write"},
		{http.MethodGet, "/api/notes/write"},
		{http.MethodPost, "/api/notes/read"},
		{http.MethodGet, "/api/not-a-route"},
	} {
		request := httptest.NewRequest(tc.method, mcpInternalBase+tc.path, strings.NewReader(`{}`))
		response, err := mcpTransport{s}.RoundTrip(request)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s: %d", tc.method, tc.path, response.StatusCode)
		}
	}
	request := httptest.NewRequest(http.MethodGet, mcpInternalBase+"/api/vault/settings", nil)
	response, err := mcpTransport{s}.RoundTrip(request)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Errorf("reading settings: %v %v", response, err)
	}
}

// A misspelled entry in the allow list would only show up as a tool that always fails.
func TestEveryMCPRouteExists(t *testing.T) {
	_, s := newMCPServer(t, nil, mcpConfig("write"))
	exists := map[string]bool{}
	if err := chi.Walk(s.self.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		exists[method+" "+strings.TrimPrefix(strings.ReplaceAll(route, "/*", ""), "/api")] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for route := range mcpRoutes {
		if !exists[route] {
			t.Errorf("%s is not a route", route)
		}
	}
}

func TestMCPTransportChecksTheContextFirst(t *testing.T) {
	_, s := newMCPServer(t, nil, mcpConfig("write"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, mcpInternalBase+"/api/notes", nil).WithContext(ctx)
	if _, err := (mcpTransport{s}).RoundTrip(request); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v", err)
	}
}

// A note far larger than the server would accept can still be on disk. The browser app
// reads it as before, and MCP declines before reading it into memory.
func TestMCPDeclinesANoteTooLargeToHold(t *testing.T) {
	server, s := newMCPServer(t, nil, mcpConfig("write"))
	abs := filepath.Join(s.currentVault().Root(), "Big.md")
	file, err := os.Create(abs)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(s.mcpResponseBytes + 1); err != nil {
		t.Fatal(err)
	}
	file.Close()
	text, failed := mcpCall(t, server.URL+"/mcp", "read_note", map[string]any{"path": "Big.md"})
	if !failed || !strings.Contains(text, "too large") {
		t.Errorf("read_note: %v %.200s", failed, text)
	}
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/api/notes/read?path=Big.md", nil)
	request.Header.Set("Authorization", "Bearer "+mcpTestServerToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusOK {
		t.Errorf("REST read: %d", response.StatusCode)
	}
}

// Before reading it, since a large note would otherwise be in memory by the time the
// recorder's limit could refuse it. An in-process call with a tiny limit shows which one
// answered: the recorder here has room.
func TestReadNoteChecksTheSizeFirst(t *testing.T) {
	_, s := newMCPServer(t, nil, mcpConfig("write"))
	if err := os.WriteFile(filepath.Join(s.currentVault().Root(), "Note.md"), []byte(strings.Repeat("x", 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, mcpInternalBase+"/api/notes/read?path=Note.md", nil)
	request = request.WithContext(context.WithValue(request.Context(), mcpResponseLimitKey{}, int64(10)))
	request.Header.Set("Authorization", "Bearer "+mcpTestServerToken)
	recorder := &mcpRecorder{header: http.Header{}, limit: 1 << 20}
	s.self.ServeHTTP(recorder, request)
	if recorder.status != http.StatusRequestEntityTooLarge || strings.Contains(recorder.body.String(), "xxx") {
		t.Errorf("%d %q", recorder.status, recorder.body.String())
	}
}

func TestOpenRequestsFollowTheMemoryLimit(t *testing.T) {
	for limit, want := range map[int64]int{math.MaxInt64: 32, 0: 32, 700 << 20: 8, 800 << 20: 8, 1 << 30: 16, 2 << 30: 16, 4 << 30: 32} {
		if got := mcpInFlight(limit); got != want {
			t.Errorf("%d: %d, want %d", limit, got, want)
		}
	}
}

func TestResponseLimitFollowsTheMemoryLimit(t *testing.T) {
	cfg := config.Config{MaxNoteBytes: 10 << 20}
	for limit, want := range map[int64]int64{
		math.MaxInt64: 64 << 20,
		358 << 20:     358 << 20 / 16,
		100 << 20:     16 << 20,
		716 << 20:     716 << 20 / 16,
		16 << 30:      64 << 20,
	} {
		if got := mcpResponseLimit(cfg, limit); got != want {
			t.Errorf("memory %d MiB: %d MiB, want %d MiB", limit>>20, got>>20, want>>20)
		}
	}
	if got := mcpResponseLimit(config.Config{MaxNoteBytes: 100 << 20}, math.MaxInt64); got != 200<<20 {
		t.Errorf("a large note limit: %d", got)
	}
}

func TestToolCallsFollowTheMemoryLimit(t *testing.T) {
	for limit, want := range map[int64]int{math.MaxInt64: 4, 0: 4, 700 << 20: 1, 1 << 30: 2, 2 << 30: 2, 4 << 30: 4} {
		if got := mcpToolCalls(limit); got != want {
			t.Errorf("%d: %d, want %d", limit, got, want)
		}
	}
}

func TestMCPRecorderDropsAnAnswerPastItsLimit(t *testing.T) {
	r := &mcpRecorder{header: http.Header{"X": {"y"}}, limit: 10}
	if _, err := r.Write([]byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Write([]byte("a")); !errors.Is(err, errMCPResponseTooLarge) {
		t.Errorf("past the limit: %v", err)
	}
	if _, err := r.Write([]byte("a")); !errors.Is(err, errMCPResponseTooLarge) {
		t.Errorf("after the limit: %v", err)
	}
	response := r.response(httptest.NewRequest(http.MethodGet, "/", nil))
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusRequestEntityTooLarge || strings.Contains(string(body), "0123") || response.Header.Get("X") != "" {
		t.Errorf("%d %q %v", response.StatusCode, body, response.Header)
	}
}

func TestSearchTakesALimit(t *testing.T) {
	server, s := newMCPServer(t, nil, mcpConfig("write"))
	var body strings.Builder
	for i := range 250 {
		fmt.Fprintf(&body, "needle %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(s.currentVault().Root(), "Many.md"), []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	search := func(query string) (int, int) {
		request, _ := http.NewRequest(http.MethodGet, server.URL+"/api/search/text?q=needle"+query, nil)
		request.Header.Set("Authorization", "Bearer "+mcpTestServerToken)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var matches []any
		_ = json.NewDecoder(response.Body).Decode(&matches)
		return response.StatusCode, len(matches)
	}
	if status, n := search(""); status != http.StatusOK || n != vault.DefaultTextSearchLimit {
		t.Errorf("no limit: %d, %d matches", status, n)
	}
	if status, n := search("&limit=240"); status != http.StatusOK || n != 240 {
		t.Errorf("limit 240: %d, %d matches", status, n)
	}
	for _, bad := range []string{"0", "-1", "x", fmt.Sprint(vault.MaxTextSearchLimit + 1)} {
		if status, _ := search("&limit=" + bad); status != http.StatusBadRequest {
			t.Errorf("limit %s: %d", bad, status)
		}
	}
}

func TestCommentsAreSignedByTheCredential(t *testing.T) {
	server, s := newMCPServer(t, nil, mcpConfig("write"))
	if err := os.WriteFile(filepath.Join(s.currentVault().Root(), "Note.md"), []byte("# Note\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	url := server.URL + "/mcp"
	if text, failed := mcpCall(t, url, "add_comment", map[string]any{"path": "Note.md", "body": "hi", "author": "The Owner"}); failed {
		t.Fatalf("add_comment: %s", text)
	}
	text, failed := mcpCall(t, url, "list_comments", map[string]any{"path": "Note.md"})
	if failed || !strings.Contains(text, `"author": "test"`) || strings.Contains(text, "The Owner") {
		t.Errorf("list_comments: %s", text)
	}
}

func TestMCPRecorderKeepsTheFirstStatus(t *testing.T) {
	r := &mcpRecorder{header: http.Header{}, limit: 10}
	r.WriteHeader(http.StatusTeapot)
	r.WriteHeader(http.StatusOK)
	_, _ = r.Write([]byte("hi"))
	response := r.response(httptest.NewRequest(http.MethodGet, "/", nil))
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusTeapot || string(body) != "hi" || response.ContentLength != 2 {
		t.Errorf("%d %q %d", response.StatusCode, body, response.ContentLength)
	}
}
