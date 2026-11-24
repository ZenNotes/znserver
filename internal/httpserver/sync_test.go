package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ZenNotes/znserver/internal/config"
	"github.com/ZenNotes/znserver/internal/vault"
	"github.com/ZenNotes/znserver/internal/watcher"
)

// The sync routes over HTTP (#1108, phase 2): the wire shapes Cloud's engine
// reads, error codes included. The engine itself drives them in
// integration/cloud-engine.

type syncClient struct {
	t      *testing.T
	server *httptest.Server
	token  string
}

func (c syncClient) do(method, path string, payload any) (int, map[string]any) {
	c.t.Helper()
	var body bytes.Buffer
	if payload != nil {
		if err := json.NewEncoder(&body).Encode(payload); err != nil {
			c.t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, c.server.URL+path, &body)
	if err != nil {
		c.t.Fatal(err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func textContent(body string) map[string]any {
	sum := sha256.Sum256([]byte(body))
	return map[string]any{
		"encoding": "utf8", "data": body, "sha256": hex.EncodeToString(sum[:]),
		"byte_length": len(body), "media_type": "text/markdown",
	}
}

func newSyncTestServer(t *testing.T) (syncClient, *vault.Vault) {
	t.Helper()
	root := t.TempDir()
	server, v := newTestServer(t, config.Config{
		VaultPath: root, DefaultVaultPath: root, Bind: "127.0.0.1:7878",
		AuthToken: "sync-token", BrowseRoots: []string{root},
	})
	return syncClient{t: t, server: server, token: "sync-token"}, v
}

func TestSyncCapabilityAndAuth(t *testing.T) {
	client, _ := newSyncTestServer(t)
	status, caps := client.do(http.MethodGet, "/api/capabilities", nil)
	if status != http.StatusOK || caps["supportsSync"] != float64(syncProtocolVersion) {
		t.Fatalf("capabilities = %d %v", status, caps)
	}
	anonymous := syncClient{t: t, server: client.server}
	for _, path := range []string{"/api/sync/manifest", "/api/sync/changes?after=0"} {
		if status, _ := anonymous.do(http.MethodGet, path, nil); status != http.StatusUnauthorized {
			t.Fatalf("%s without the token = %d", path, status)
		}
	}
	if status, _ := anonymous.do(http.MethodPost, "/api/sync/mutations", map[string]any{"mutations": []any{}}); status != http.StatusUnauthorized {
		t.Fatalf("mutations without the token = %d", status)
	}
}

func TestSyncRoundTripOverHTTP(t *testing.T) {
	client, v := newSyncTestServer(t)
	status, manifest := client.do(http.MethodGet, "/api/sync/manifest?page=1&perPage=250&include_content=1", nil)
	if status != http.StatusOK || manifest["vault_id"] == "" || manifest["cursor"] != float64(0) || manifest["next_page"] != nil {
		t.Fatalf("manifest = %d %v", status, manifest)
	}
	vaultID := manifest["vault_id"].(string)

	status, result := client.do(http.MethodPost, "/api/sync/mutations?vault_id="+vaultID, map[string]any{"mutations": []any{
		map[string]any{"operation_id": "op-1", "item_id": "item-1", "base_revision": nil, "type": "upsert",
			"path": "inbox/From Sync.md", "kind": "text", "content": textContent("# From sync\n")},
	}})
	acks, _ := result["acknowledged"].([]any)
	if status != http.StatusOK || len(acks) != 1 || result["cursor"] != float64(1) {
		t.Fatalf("mutations = %d %v", status, result)
	}
	ack := acks[0].(map[string]any)
	if ack["operation_id"] != "op-1" || ack["item_id"] != "item-1" || ack["revision"] != float64(1) || ack["sequence"] != float64(1) {
		t.Fatalf("ack = %v", ack)
	}

	// A write through the ordinary note API shows up in the change feed.
	if _, err := v.WriteNote("inbox/From Sync.md", "# Edited on the web\n"); err != nil {
		t.Fatal(err)
	}
	status, changes := client.do(http.MethodGet, "/api/sync/changes?after=1&limit=100&vault_id="+vaultID, nil)
	data, _ := changes["data"].([]any)
	if status != http.StatusOK || len(data) != 1 || changes["has_more"] != false || changes["cursor"] != float64(2) {
		t.Fatalf("changes = %d %v", status, changes)
	}
	change := data[0].(map[string]any)
	content, _ := change["content"].(map[string]any)
	if change["type"] != "upsert" || change["item_id"] != "item-1" || change["revision"] != float64(2) ||
		change["previous_path"] != nil || content["data"] != "# Edited on the web\n" {
		t.Fatalf("change = %v", change)
	}

	// A stale base answers per mutation, with the item's current revision.
	status, result = client.do(http.MethodPost, "/api/sync/mutations?vault_id="+vaultID, map[string]any{"mutations": []any{
		map[string]any{"operation_id": "op-2", "item_id": "item-1", "base_revision": 1, "type": "upsert",
			"path": "inbox/From Sync.md", "kind": "text", "content": textContent("stale")},
	}})
	conflicts, _ := result["conflicts"].([]any)
	if status != http.StatusOK || len(conflicts) != 1 {
		t.Fatalf("stale = %d %v", status, result)
	}
	conflict := conflicts[0].(map[string]any)
	if conflict["code"] != "REVISION_CONFLICT" || conflict["current_revision"] != float64(2) || conflict["current_path"] != "inbox/From Sync.md" {
		t.Fatalf("conflict = %v", conflict)
	}
}

func TestSyncErrorShapesTheEngineReads(t *testing.T) {
	client, v := newSyncTestServer(t)
	id, err := v.SyncVaultID()
	if err != nil {
		t.Fatal(err)
	}
	// The change feed and mutations must name the vault.
	if status, _ := client.do(http.MethodGet, "/api/sync/changes?after=0", nil); status != http.StatusBadRequest {
		t.Fatalf("changes without vault_id = %d", status)
	}
	if status, _ := client.do(http.MethodPost, "/api/sync/mutations", map[string]any{"mutations": []any{}}); status != http.StatusBadRequest {
		t.Fatalf("mutations without vault_id = %d", status)
	}
	// 422 naming only a path: the engine sets that file aside (#1004).
	status, body := client.do(http.MethodPost, "/api/sync/mutations?vault_id="+id, map[string]any{"mutations": []any{
		map[string]any{"operation_id": "op", "item_id": "img", "base_revision": nil, "type": "upsert", "path": "assets/a.png",
			"kind": "binary", "content": map[string]any{"encoding": "base64", "data": "AA==", "sha256": "x", "byte_length": 1, "media_type": "image/png"}},
	}})
	fields, _ := body["errors"].(map[string]any)
	if status != http.StatusUnprocessableEntity || body["code"] != "VALIDATION_FAILED" || len(fields) != 1 || fields["mutations.0.path"] == nil {
		t.Fatalf("binary upsert = %d %v", status, body)
	}
	// 410 CURSOR_EXPIRED for a cursor the log does not have.
	status, body = client.do(http.MethodGet, "/api/sync/changes?after=99&vault_id="+id, nil)
	if status != http.StatusGone || body["code"] != "CURSOR_EXPIRED" {
		t.Fatalf("future cursor = %d %v", status, body)
	}
	// 409 VAULT_MISMATCH for a copy of another vault.
	status, body = client.do(http.MethodGet, "/api/sync/changes?after=0&vault_id=someone-else", nil)
	if status != http.StatusConflict || body["code"] != "VAULT_MISMATCH" {
		t.Fatalf("other vault = %d %v", status, body)
	}
	status, body = client.do(http.MethodGet, "/api/sync/changes?after=zero&vault_id="+id, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("bad cursor = %d %v", status, body)
	}
	status, body = client.do(http.MethodGet, "/api/sync/manifest?page=9223372036854775807&perPage=1000", nil)
	if status != http.StatusOK || len(body["data"].([]any)) != 0 || body["next_page"] != nil {
		t.Fatalf("a page past the end = %d %v", status, body)
	}
}

// The maintenance loop records an edit made outside the server, from the
// watcher's hint, without any client asking.
func TestSyncMaintenanceReconcilesOnAWatcherHint(t *testing.T) {
	root := t.TempDir()
	v, err := vault.New(root, vault.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.SyncVaultID(); err != nil {
		t.Fatal(err)
	}
	w := watcher.StartOrDisabled(root, false)
	t.Cleanup(w.Close)
	if !w.Active() {
		t.Skip("no live watcher on this host")
	}
	s := New(v, w, nil, config.Config{VaultPath: root})
	s.StartSyncMaintenance(context.Background())
	t.Cleanup(v.Close)
	t.Cleanup(s.StopSyncMaintenance)

	if err := os.WriteFile(filepath.Join(root, "inbox", "Shell.md"), []byte("from a shell\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		page, err := v.SyncManifest(2, 1000, false) // page 2: no reconcile of its own
		if err != nil {
			t.Fatal(err)
		}
		if page.Cursor > 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the hint never led to a reconcile")
}

// After /api/vault/select the sync routes answer for the new vault, under its
// own vault_id, and the old id is refused.
func TestSyncFollowsAVaultSwitch(t *testing.T) {
	t.Setenv("ZENNOTES_VAULT_PATH", "")
	t.Setenv("ZENNOTES_CONFIG_PATH", filepath.Join(t.TempDir(), "server.json"))
	first, second := t.TempDir(), t.TempDir()
	v, err := vault.New(first, vault.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s := New(v, nil, nil, config.Config{
		VaultPath: first, DefaultVaultPath: first, Bind: "127.0.0.1:7878",
		AuthToken: "switch-token", BrowseRoots: []string{first, second},
	})
	s.StartSyncMaintenance(context.Background())
	t.Cleanup(s.StopSyncMaintenance)
	server := httptest.NewServer(s.Router())
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		if w := s.currentWatcher(); w != nil {
			w.Close()
		}
	})
	client := syncClient{t: t, server: server, token: "switch-token"}
	_, before := client.do(http.MethodGet, "/api/sync/manifest", nil)
	oldID := before["vault_id"].(string)
	if status, body := client.do(http.MethodPost, "/api/vault/select", map[string]string{"path": second}); status != http.StatusOK {
		t.Fatalf("select = %d %v", status, body)
	}
	if status, body := client.do(http.MethodGet, "/api/sync/changes?after=0&vault_id="+oldID, nil); status != http.StatusConflict || body["code"] != "VAULT_MISMATCH" {
		t.Fatalf("old vault id after a switch = %d %v", status, body)
	}
	_, after := client.do(http.MethodGet, "/api/sync/manifest", nil)
	if after["vault_id"] == oldID {
		t.Fatal("the new vault has its own id")
	}
}
