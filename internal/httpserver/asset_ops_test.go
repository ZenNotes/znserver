package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/ZenNotes/zennotes/apps/server/internal/config"
)

// TestAssetRenameAndMoveEndpoints exercises the full HTTP wiring for the asset
// mutation endpoints added for remote vaults (#379): rename in place, then move
// into a folder, asserting the JSON field contract the web bridge relies on.
func TestAssetRenameAndMoveEndpoints(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "pic.png"), []byte("PNG"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, _ := newTestServer(t, config.Config{
		VaultPath:        root,
		DefaultVaultPath: root,
		Bind:             "127.0.0.1:7878",
		AuthToken:        "secret-token",
		BrowseRoots:      []string{root},
	})
	jar := loginAndJar(t, server, "secret-token")
	client := &http.Client{Jar: jar}

	postJSON := func(path string, payload map[string]string) (string, int) {
		t.Helper()
		body, _ := json.Marshal(payload)
		resp, err := client.Post(server.URL+path, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer resp.Body.Close()
		var meta struct {
			Path string `json:"path"`
		}
		if resp.StatusCode == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
				t.Fatalf("decode %s response: %v", path, err)
			}
		}
		return meta.Path, resp.StatusCode
	}

	gotPath, status := postJSON("/api/assets/rename", map[string]string{"path": "assets/pic.png", "name": "shot.png"})
	if status != http.StatusOK {
		t.Fatalf("rename status = %d, want 200", status)
	}
	if gotPath != "assets/shot.png" {
		t.Fatalf("rename path = %q, want assets/shot.png", gotPath)
	}

	gotPath, status = postJSON("/api/assets/move", map[string]string{"path": "assets/shot.png", "targetDir": "media"})
	if status != http.StatusOK {
		t.Fatalf("move status = %d, want 200", status)
	}
	if gotPath != "media/shot.png" {
		t.Fatalf("move path = %q, want media/shot.png", gotPath)
	}
	if _, err := os.Stat(filepath.Join(root, "media", "shot.png")); err != nil {
		t.Errorf("moved file missing on disk: %v", err)
	}
}

// TestFolderColorsPersistOverHTTP is the reporter's exact scenario (#379): a
// recolor saved from the web client must survive the /vault/settings round-trip
// instead of being silently dropped by the server.
func TestFolderColorsPersistOverHTTP(t *testing.T) {
	root := t.TempDir()
	server, _ := newTestServer(t, config.Config{
		VaultPath:        root,
		DefaultVaultPath: root,
		Bind:             "127.0.0.1:7878",
		AuthToken:        "secret-token",
		BrowseRoots:      []string{root},
	})
	jar := loginAndJar(t, server, "secret-token")
	client := &http.Client{Jar: jar}

	payload := map[string]any{
		"primaryNotesLocation": "inbox",
		"folderColors":         map[string]string{"inbox:Projects": "violet"},
	}
	body, _ := json.Marshal(payload)
	resp, err := client.Post(server.URL+"/api/vault/settings", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/vault/settings: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set settings status = %d, want 200", resp.StatusCode)
	}

	getResp, err := client.Get(server.URL + "/api/vault/settings")
	if err != nil {
		t.Fatalf("GET /api/vault/settings: %v", err)
	}
	defer getResp.Body.Close()
	var got struct {
		FolderColors map[string]string `json:"folderColors"`
	}
	if err := json.NewDecoder(getResp.Body).Decode(&got); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if got.FolderColors["inbox:Projects"] != "violet" {
		t.Fatalf("folderColors dropped over HTTP round-trip: %v", got.FolderColors)
	}
}

// TestAssetDeleteRestorePurgeEndpoints exercises the deleted-assets store over
// HTTP: delete parks the file with an undo token, the list surfaces it, restore
// brings it back deduped, and purge/empty leave the store clean. This is the
// contract the desktop remote workspace and the web bridge share.
func TestAssetDeleteRestorePurgeEndpoints(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "pic.png"), []byte("PNG"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, _ := newTestServer(t, config.Config{
		VaultPath:        root,
		DefaultVaultPath: root,
		Bind:             "127.0.0.1:7878",
		AuthToken:        "secret-token",
		BrowseRoots:      []string{root},
	})
	jar := loginAndJar(t, server, "secret-token")
	client := &http.Client{Jar: jar}

	post := func(path string, payload any) *http.Response {
		t.Helper()
		body, _ := json.Marshal(payload)
		resp, err := client.Post(server.URL+path, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		return resp
	}

	resp := post("/api/assets/delete", map[string]string{"path": "assets/pic.png"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d, want 200", resp.StatusCode)
	}
	var deleted struct {
		Path      string `json:"path"`
		Name      string `json:"name"`
		UndoToken string `json:"undoToken"`
		DeletedAt string `json:"deletedAt"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&deleted); err != nil {
		t.Fatalf("decode delete response: %v", err)
	}
	resp.Body.Close()
	if deleted.Path != "assets/pic.png" || deleted.UndoToken == "" || deleted.DeletedAt == "" {
		t.Fatalf("delete response = %+v, want path+token+timestamp", deleted)
	}
	if _, err := os.Stat(filepath.Join(root, "assets", "pic.png")); !os.IsNotExist(err) {
		t.Fatal("file still present after HTTP delete")
	}

	listResp, err := client.Get(server.URL + "/api/assets/deleted")
	if err != nil {
		t.Fatal(err)
	}
	var listed []struct {
		UndoToken string `json:"undoToken"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listed); err != nil {
		t.Fatalf("decode deleted list: %v", err)
	}
	listResp.Body.Close()
	if len(listed) != 1 || listed[0].UndoToken != deleted.UndoToken {
		t.Fatalf("deleted list = %+v, want the parked entry", listed)
	}

	resp = post("/api/assets/restore", deleted)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("restore status = %d, want 200", resp.StatusCode)
	}
	var restored struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&restored); err != nil {
		t.Fatalf("decode restore response: %v", err)
	}
	resp.Body.Close()
	if restored.Path != "assets/pic.png" {
		t.Fatalf("restored path = %q, want assets/pic.png", restored.Path)
	}
	if _, err := os.Stat(filepath.Join(root, "assets", "pic.png")); err != nil {
		t.Fatalf("restored file missing: %v", err)
	}

	// Round two: delete again, then purge instead of restore.
	resp = post("/api/assets/delete", map[string]string{"path": "assets/pic.png"})
	if err := json.NewDecoder(resp.Body).Decode(&deleted); err != nil {
		t.Fatalf("decode second delete: %v", err)
	}
	resp.Body.Close()
	resp = post("/api/assets/purge", map[string]string{"undoToken": deleted.UndoToken})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("purge status = %d, want 204", resp.StatusCode)
	}
	resp.Body.Close()
	resp = post("/api/assets/empty-deleted", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("empty status = %d, want 204", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestAssetDuplicateEndpoint checks the copy lands next to the source with the
// shared " copy" naming.
func TestAssetDuplicateEndpoint(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "pic.png"), []byte("PNG"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, _ := newTestServer(t, config.Config{
		VaultPath:        root,
		DefaultVaultPath: root,
		Bind:             "127.0.0.1:7878",
		AuthToken:        "secret-token",
		BrowseRoots:      []string{root},
	})
	jar := loginAndJar(t, server, "secret-token")
	client := &http.Client{Jar: jar}

	body, _ := json.Marshal(map[string]string{"path": "assets/pic.png"})
	resp, err := client.Post(server.URL+"/api/assets/duplicate", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("duplicate status = %d, want 200", resp.StatusCode)
	}
	var meta struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		t.Fatal(err)
	}
	if meta.Path != "assets/pic copy.png" {
		t.Fatalf("duplicate path = %q, want assets/pic copy.png", meta.Path)
	}
}
