package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"runtime/debug"
	"strings"

	"github.com/ZenNotes/tui/mcpserver"
	"github.com/ZenNotes/znserver"
	"github.com/ZenNotes/znserver/internal/config"
	"github.com/go-chi/chi/v5"
)

// mcpInternalBase addresses the in-process calls below. It never reaches a network
// or, through mcpserver, a client.
const mcpInternalBase = "http://zennotes.internal"

// mcpResponseLimit is the largest answer an in-process call may hold in memory. A note
// can be larger than the server would accept today, if it was put there some other way.
// An answer can sit in memory about twice over until its client reads it, one for each
// open request, so a memory limit holds this down, to no less than a typical large note.
func mcpResponseLimit(cfg config.Config, memoryLimit int64) int64 {
	limit := max(64<<20, 2*cfg.MaxNoteBytes)
	if memoryLimit > 0 && memoryLimit < math.MaxInt64 {
		limit = min(limit, max(16<<20, memoryLimit/int64(2*mcpInFlight(memoryLimit))))
	}
	return limit
}

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
	memoryLimit := debug.SetMemoryLimit(-1)
	s.mcpResponseBytes = mcpResponseLimit(cfg, memoryLimit)
	handler, err := mcpserver.NewHandler(mcpserver.Options{
		Transport:        mcpTransport{s},
		BaseURL:          mcpInternalBase + cfg.BasePath,
		Credentials:      credentials,
		AllowedOrigins:   m.AllowedOrigins,
		MaxNoteBytes:     cfg.MaxNoteBytes,
		MaxToolCalls:     mcpToolCalls(memoryLimit),
		MaxInFlight:      mcpInFlight(memoryLimit),
		MaxResponseBytes: s.mcpResponseBytes,
		Version:          znserver.Version(),
		Logf:             log.Printf,
	})
	if err != nil {
		return err
	}
	s.mcp = handler
	s.mcpTokens = tokens
	return nil
}

// mcpToolCalls is how many tool calls may run at once. One can hold a listing and a
// search's matches in memory, so on a small server they take turns more strictly.
func mcpToolCalls(memoryLimit int64) int {
	if memoryLimit > 0 && memoryLimit <= 800<<20 {
		return 1
	}
	if memoryLimit > 0 && memoryLimit <= 2<<30 {
		return 2
	}
	return 4
}

// mcpInFlight is how many MCP requests may be open at once. A tool's answer stays in
// memory until its client reads it, which a client can choose not to do.
func mcpInFlight(memoryLimit int64) int {
	if memoryLimit > 0 && memoryLimit <= 800<<20 {
		return 8
	}
	if memoryLimit > 0 && memoryLimit <= 2<<30 {
		return 16
	}
	return 32
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

// mcpRoutes are the API routes the MCP tools call. Anything else is refused, so a bug
// in a tool cannot switch the vault, change the server's token, run a workflow or browse
// the filesystem, and a route added to the server later stays out until it is added here.
var mcpRoutes = map[string]bool{
	"GET /capabilities":       true,
	"GET /vault":              true,
	"GET /vault/settings":     true,
	"GET /notes":              true,
	"GET /notes/read":         true,
	"POST /notes/write":       true,
	"POST /notes/create":      true,
	"POST /notes/rename":      true,
	"POST /notes/delete":      true,
	"POST /notes/trash":       true,
	"POST /notes/restore":     true,
	"POST /notes/empty-trash": true,
	"POST /notes/archive":     true,
	"POST /notes/unarchive":   true,
	"POST /notes/duplicate":   true,
	"POST /notes/move":        true,
	"GET /folders":            true,
	"POST /folders/create":    true,
	"POST /folders/rename":    true,
	"POST /folders/delete":    true,
	"GET /assets":             true,
	"GET /assets/raw":         true,
	"GET /assets/deleted":     true,
	"POST /assets/upload":     true,
	"POST /assets/rename":     true,
	"POST /assets/move":       true,
	"POST /assets/duplicate":  true,
	"POST /assets/delete":     true,
	"POST /assets/restore":    true,
	"GET /comments/read":      true,
	"POST /comments/write":    true,
	"GET /search/text":        true,
	"GET /tasks":              true,
	"GET /tasks/for":          true,
	"GET /templates":          true,
	"GET /templates/read":     true,
}

func mcpRouteAllowed(method, path string) bool {
	return mcpRoutes[method+" "+strings.TrimPrefix(path, "/api")]
}

func (t mcpTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s := t.s
	if req.Body != nil {
		defer req.Body.Close()
	}
	// Reached with nothing left of the call's time, there is no one to answer.
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	cfg := s.currentConfig()
	path := strings.TrimPrefix(req.URL.Path, cfg.BasePath)
	if !mcpRouteAllowed(req.Method, path) {
		return mcpResponse(req, http.StatusForbidden, "not available over MCP"), nil
	}
	limit := s.mcpResponseBytes
	// The context holds the outer request's chi route state, which would route this
	// call straight back to /mcp, where the server's token gets a 401.
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, nil)
	inner := req.Clone(context.WithValue(ctx, mcpResponseLimitKey{}, limit))
	inner.RequestURI = req.URL.RequestURI()
	if inner.Body == nil {
		inner.Body = http.NoBody
	}
	inner.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.AuthToken))
	recorder := &mcpRecorder{header: http.Header{}, limit: limit}
	s.self.ServeHTTP(recorder, inner)
	return recorder.response(req), nil
}

// mcpResponseLimitKey marks a request as an in-process MCP call, and holds how large
// its answer may be, for a handler that would otherwise read a whole file first.
type mcpResponseLimitKey struct{}

// tooLargeForMCP refuses a note an MCP tool could not hold in memory. A note the
// browser app is reading is not limited.
func tooLargeForMCP(w http.ResponseWriter, r *http.Request, size int64) bool {
	limit, ok := r.Context().Value(mcpResponseLimitKey{}).(int64)
	if !ok || size <= limit {
		return false
	}
	http.Error(w, "too large to read over MCP", http.StatusRequestEntityTooLarge)
	return true
}

func mcpResponse(req *http.Request, status int, text string) *http.Response {
	recorder := &mcpRecorder{header: http.Header{}, limit: int64(len(text))}
	recorder.WriteHeader(status)
	_, _ = recorder.Write([]byte(text))
	return recorder.response(req)
}

// mcpRecorder holds one in-process response in memory, up to a limit. Past it, the
// answer is dropped and the call gets a 413.
type mcpRecorder struct {
	header   http.Header
	status   int
	body     bytes.Buffer
	limit    int64
	overflow bool
}

var errMCPResponseTooLarge = errors.New("response too large for MCP")

func (r *mcpRecorder) Header() http.Header { return r.header }

func (r *mcpRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}

func (r *mcpRecorder) Write(data []byte) (int, error) {
	r.WriteHeader(http.StatusOK)
	if r.overflow || int64(r.body.Len())+int64(len(data)) > r.limit {
		r.overflow = true
		r.body = bytes.Buffer{}
		return 0, errMCPResponseTooLarge
	}
	return r.body.Write(data)
}

func (r *mcpRecorder) response(req *http.Request) *http.Response {
	r.WriteHeader(http.StatusOK)
	if r.overflow {
		r.status = http.StatusRequestEntityTooLarge
		r.header = http.Header{}
		r.body.WriteString("too large to read over MCP")
	}
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
