package httpserver

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZenNotes/zennotes/apps/server/internal/config"
	"github.com/ZenNotes/zennotes/apps/server/internal/vault"
	"github.com/ZenNotes/zennotes/apps/server/internal/watcher"
	"github.com/coder/websocket"
	"net/http/httptest"
)

// TestWatchSubscriberSurvivesPingCycles is the regression test for the wedge
// that produced "changes don't appear until I refresh": watchWS never read
// from the connection, so the client's pong was never processed and the first
// keepalive Ping blocked forever — every subscriber went silent 25 seconds
// after connecting while the connection stayed ESTABLISHED. Here the ping
// interval is shrunk so several cycles pass in milliseconds, then a file
// change must still reach the subscriber.
func TestWatchSubscriberSurvivesPingCycles(t *testing.T) {
	oldInterval := watchPingInterval
	watchPingInterval = 30 * time.Millisecond
	defer func() { watchPingInterval = oldInterval }()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "inbox"), 0o700); err != nil {
		t.Fatal(err)
	}
	v, err := vault.New(root, vault.Options{})
	if err != nil {
		t.Fatal(err)
	}
	w := watcher.StartOrDisabled(root, false)
	if !w.Active() {
		t.Skip("filesystem watching unavailable in this environment")
	}
	t.Cleanup(w.Close)

	cfg := config.Config{
		VaultPath:        root,
		DefaultVaultPath: root,
		Bind:             "127.0.0.1:7878",
		AuthToken:        "secret-token",
		BrowseRoots:      []string{root},
	}
	server := httptest.NewServer(New(v, w, nil, cfg).Router())
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/watch"
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer secret-token"}},
	})
	if err != nil {
		t.Fatalf("dial watch socket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Let well over a handful of ping cycles pass. The wedged loop hung on
	// the very first one.
	time.Sleep(300 * time.Millisecond)

	if err := os.WriteFile(filepath.Join(root, "inbox", "Late.md"), []byte("# Late"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Read pumps control frames client-side too, so this both answers any
	// in-flight ping and receives the change event.
	_, payload, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("no event after ping cycles (the pre-fix wedge): %v", err)
	}
	if !strings.Contains(string(payload), "Late.md") {
		t.Fatalf("event payload = %s, want the Late.md change", payload)
	}
}
