package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/ZenNotes/tui/mcpserver"
	"github.com/ZenNotes/znserver"
	"github.com/ZenNotes/znserver/internal/config"
	"github.com/go-chi/chi/v5"
)

// mcpInternalBase addresses the in-process calls below. It never reaches a network
// or, through mcpserver, a client.
const mcpInternalBase = "http://zennotes.internal"

const mcpVaultSwitched = "The ZenNotes server switched to another vault, so nothing is shared until it restarts."

// EnableMCP turns on the built-in MCP endpoint, for Router to route at /mcp. An
// error means the server should not start.
//
// MCP clients use their own tokens, which open /mcp and nothing else. The server's
// token and browser sessions do not open /mcp, so a leaked MCP token can never reach
// the REST API and a restricted agent never holds the unrestricted token. That only
// means something if the REST API needs a token at all, which is why one is required.
func (s *Server) EnableMCP(m config.MCPConfig) error {
	if !m.Enabled {
		return nil
	}
	cfg := s.currentConfig()
	serverToken := strings.TrimSpace(cfg.AuthToken)
	if serverToken == "" {
		return errors.New("ZENNOTES_MCP_ENABLED needs ZENNOTES_AUTH_TOKEN set too: with an open REST API, an MCP scope restricts nothing")
	}
	serverSum := mcpserver.HashToken(serverToken)
	credentials := make([]mcpserver.Credential, 0, len(m.Credentials))
	tokens := make(map[[sha256.Size]byte]bool, len(m.Credentials))
	for _, c := range m.Credentials {
		if c.TokenSHA256 == serverSum {
			return fmt.Errorf("MCP credential %q uses the server's own token: give it one of its own", c.Name)
		}
		tokens[c.TokenSHA256] = true
		credentials = append(credentials, mcpserver.Credential{
			Name:        c.Name,
			TokenSHA256: c.TokenSHA256,
			Access:      c.Access,
			Allow:       c.Allow,
			Deny:        c.Deny,
		})
	}
	s.mcpVaultRoot = s.currentVault().Root()
	handler, err := mcpserver.NewHandler(mcpserver.Options{
		Transport: mcpTransport{s},
		BaseURL:   mcpInternalBase + cfg.BasePath,
		Available: func() error {
			if s.mcpVaultMoved() {
				return errors.New(mcpVaultSwitched)
			}
			return nil
		},
		Credentials:    credentials,
		AllowedOrigins: m.AllowedOrigins,
		MaxNoteBytes:   cfg.MaxNoteBytes,
		Version:        znserver.Version(),
		Logf:           log.Printf,
	})
	if err != nil {
		return err
	}
	s.mcp = handler
	s.mcpTokens = tokens
	return nil
}

// mcpVaultMoved says the server is serving another vault than the one MCP started
// on. An agent that read notes in one vault and writes after a switch would change
// a note at the same path in the other. MCP requests have no client to warn, since
// each stands alone, so nothing is shared until the server restarts or switches back.
func (s *Server) mcpVaultMoved() bool {
	current := s.currentVault().Root()
	if current == s.mcpVaultRoot {
		return false
	}
	// A vault switched back to can be spelled differently than the one started on,
	// through /var and /private/var on macOS for one.
	currentInfo, currentErr := os.Stat(current)
	startInfo, startErr := os.Stat(s.mcpVaultRoot)
	return currentErr != nil || startErr != nil || !os.SameFile(currentInfo, startInfo)
}

// mcpTokenTaken says a token is an MCP credential's. The server's own token must
// never become one, or it would open /mcp and the REST API both.
func (s *Server) mcpTokenTaken(token string) bool {
	return s.mcpTokens[mcpserver.HashToken(token)]
}

// mcpTransport lets the MCP tools call this server's API without the network, so
// the handlers remain the one implementation that owns the vault: file modes, size
// limits, link rewriting and every future rule apply to MCP automatically. It alone
// holds the server's token, read fresh on each call, so rotation just works.
type mcpTransport struct{ s *Server }

// mcpRefusedRoute names API routes no MCP tool has any business calling. No tool
// does; this is here so that a bug in a tool still cannot switch the vault, change
// the server's token or browse the filesystem.
func mcpRefusedRoute(method, path string) bool {
	path = strings.TrimPrefix(path, "/api")
	for _, prefix := range []string{"/session", "/vault/select", "/fs/", "/watch", "/demo/", "/workflows"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return method != http.MethodGet && path == "/vault/settings"
}

func (t mcpTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s := t.s
	if req.Body != nil {
		defer req.Body.Close()
	}
	cfg := s.currentConfig()
	path := strings.TrimPrefix(req.URL.Path, cfg.BasePath)
	if mcpRefusedRoute(req.Method, path) {
		return mcpResponse(req, http.StatusForbidden, "not available over MCP"), nil
	}
	// Checked on every call as well as before each tool, so a tool that reads and
	// then writes cannot have the second half land in another vault.
	if s.mcpVaultMoved() {
		return mcpResponse(req, http.StatusConflict, mcpVaultSwitched), nil
	}
	// The context holds the outer request's chi route state, which would route this
	// call straight back to /mcp, where the server's token gets a 401.
	inner := req.Clone(context.WithValue(req.Context(), chi.RouteCtxKey, nil))
	inner.RequestURI = req.URL.RequestURI()
	if inner.Body == nil {
		inner.Body = http.NoBody
	}
	inner.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.AuthToken))
	recorder := &mcpRecorder{header: http.Header{}}
	s.self.ServeHTTP(recorder, inner)
	return recorder.response(req), nil
}

func mcpResponse(req *http.Request, status int, text string) *http.Response {
	recorder := &mcpRecorder{header: http.Header{}}
	recorder.WriteHeader(status)
	_, _ = recorder.Write([]byte(text))
	return recorder.response(req)
}

// mcpRecorder holds one in-process response in memory.
type mcpRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *mcpRecorder) Header() http.Header { return r.header }

func (r *mcpRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}

func (r *mcpRecorder) Write(data []byte) (int, error) {
	r.WriteHeader(http.StatusOK)
	return r.body.Write(data)
}

func (r *mcpRecorder) response(req *http.Request) *http.Response {
	r.WriteHeader(http.StatusOK)
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", r.status, http.StatusText(r.status)),
		StatusCode:    r.status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        r.header,
		Body:          io.NopCloser(&r.body),
		ContentLength: int64(r.body.Len()),
		Request:       req,
	}
}
