package httpserver

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/ZenNotes/zennotes/apps/server/internal/config"
)

// The three answers /api/notes/read can give about a path, which clients read
// as three different things. Databases are composed from these reads, where
// "absent" means "adopt this bare CSV" and "failed" means "stop", so a status
// that blurs them is a data-loss bug rather than a cosmetic one (#kta report).
func TestReadNoteStatusesDistinguishMissingFromBroken(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "inbox"), 0o755); err != nil {
		t.Fatalf("mkdir inbox: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "inbox", "Real.md"), []byte("# Real\n"), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}
	// A database folder, which is exactly the shape a client asks about when
	// it reads `<Name>.base/data.csv`.
	if err := os.MkdirAll(filepath.Join(root, "inbox", "Db.base"), 0o755); err != nil {
		t.Fatalf("mkdir db: %v", err)
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

	get := func(t *testing.T, path string) int {
		t.Helper()
		resp, err := client.Get(server.URL + "/api/notes/read?path=" + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if got := get(t, "inbox%2FReal.md"); got != http.StatusOK {
		t.Errorf("existing note: got %d, want 200", got)
	}
	// Absence is the caller's answer, not a failure: a client that cannot see
	// this cannot create a database, because naming one probes for a free name.
	if got := get(t, "inbox%2FDb.base%2Fdata.csv"); got != http.StatusNotFound {
		t.Errorf("missing file: got %d, want 404", got)
	}
	// Reading a directory as a file is a malformed request, not a broken
	// server. It answered 500 before, which sent a bug report chasing a
	// server that was fine.
	if got := get(t, "inbox%2FDb.base"); got != http.StatusBadRequest {
		t.Errorf("directory read: got %d, want 400", got)
	}
}

// Clients otherwise have to probe for this behavior at runtime, so the server
// states it. Its absence is what marks a server from before 2.20.2.
func TestCapabilitiesReportMissingAsNotFound(t *testing.T) {
	root := t.TempDir()
	server, _ := newTestServer(t, config.Config{
		VaultPath:        root,
		DefaultVaultPath: root,
		Bind:             "127.0.0.1:7878",
		BrowseRoots:      []string{root},
	})

	resp, err := http.Get(server.URL + "/api/capabilities")
	if err != nil {
		t.Fatalf("GET /api/capabilities: %v", err)
	}
	defer resp.Body.Close()

	var caps map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&caps); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if caps["reportsMissingAsNotFound"] != true {
		t.Errorf("reportsMissingAsNotFound: got %v, want true", caps["reportsMissingAsNotFound"])
	}
}
