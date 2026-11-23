package httpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ZenNotes/znserver/internal/config"
	"github.com/ZenNotes/znserver/internal/vault"
)

// After /api/vault/select the new watcher must sort change events by the new
// vault's own systemFolderPaths. It was started with no folder layout at all,
// so a vault that keeps Trash in deleted/ reported a note trashed there as an
// ordinary note until vault.json next changed.
func TestVaultSwitchSeedsTheWatcherWithTheVaultsFolderPaths(t *testing.T) {
	t.Setenv("ZENNOTES_VAULT_PATH", "")
	t.Setenv("ZENNOTES_CONFIG_PATH", filepath.Join(t.TempDir(), "server.json"))

	first := t.TempDir()
	second := t.TempDir()
	if err := os.MkdirAll(filepath.Join(second, ".zennotes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(second, ".zennotes", "vault.json"),
		[]byte(`{"systemFolderPaths":{"trash":"deleted"}}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	// The folder exists before the switch, so the new watcher watches it from
	// the start and the test does not race a directory being added.
	if err := os.MkdirAll(filepath.Join(second, "deleted"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		VaultPath:        first,
		DefaultVaultPath: first,
		Bind:             "127.0.0.1:7878",
		AuthToken:        "switch-token",
		BrowseRoots:      []string{first, second},
	}
	v, err := vault.New(first, vault.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s := New(v, nil, nil, cfg)
	server := httptest.NewServer(s.Router())
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		if w := s.currentWatcher(); w != nil {
			w.Close()
		}
	})

	status, raw := switchRequest(t, server, http.MethodPost, "/api/vault/select", map[string]string{"path": second})
	if status != http.StatusOK {
		t.Fatalf("vault select: %d %s", status, raw)
	}
	w := s.currentWatcher()
	if w == nil || !w.Active() {
		t.Skip("no live watcher on this host")
	}
	events, unsubscribe := w.Subscribe()
	defer unsubscribe()

	if err := os.WriteFile(filepath.Join(second, "deleted", "Gone.md"), []byte("gone\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Path != "deleted/Gone.md" {
				continue
			}
			if ev.Folder != vault.FolderTrash {
				t.Fatalf("event for deleted/Gone.md has folder %q, want %q (the vault's remapped Trash)", ev.Folder, vault.FolderTrash)
			}
			return
		case <-deadline:
			t.Fatal("no watcher event for deleted/Gone.md after the vault switch")
		}
	}
}

func switchRequest(t *testing.T, server *httptest.Server, method, path string, body any) (int, string) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, server.URL+path, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer switch-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}
