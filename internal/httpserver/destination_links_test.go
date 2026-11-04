package httpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ZenNotes/znserver/internal/config"
)

// The routes answer a destination through a link with a 400 a client can show,
// not the 500 a failed write would give, and leave the link's target alone.
func TestRoutesRefuseADestinationThroughALink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows")
	}
	root := t.TempDir()
	for rel, body := range map[string]string{
		".zennotes/vault.json": `{"primaryNotesLocation":"root"}`,
		"Projects/Plan.md":     "# Plan\n",
		"Projects/Sub/Step.md": "# Step\n",
		"Projects/pic.png":     "png",
		"Journal/Diary.md":     "# Diary\n",
	} {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join("..", "Journal"), filepath.Join(root, "Projects", "inner")); err != nil {
		t.Fatal(err)
	}
	server, _ := newTestServer(t, config.Config{
		VaultPath:        root,
		DefaultVaultPath: root,
		Bind:             "127.0.0.1:7878",
		AuthToken:        "secret-token",
		BrowseRoots:      []string{root},
	})
	client := &http.Client{Jar: loginAndJar(t, server, "secret-token")}

	for _, tc := range []struct {
		route string
		body  map[string]string
	}{
		{"/api/notes/create", map[string]string{"folder": "inbox", "title": "Via", "subpath": "Projects/inner"}},
		{"/api/folders/create", map[string]string{"folder": "inbox", "subpath": "Projects/inner/Made"}},
		{"/api/notes/move", map[string]string{"path": "Projects/Plan.md", "targetFolder": "inbox", "targetSubpath": "Projects/inner"}},
		{"/api/folders/rename", map[string]string{"folder": "inbox", "oldSubpath": "Projects/Sub", "newSubpath": "Projects/inner/Sub"}},
		{"/api/assets/move", map[string]string{"path": "Projects/pic.png", "targetDir": "Projects/inner"}},
	} {
		t.Run(tc.route, func(t *testing.T) {
			raw, _ := json.Marshal(tc.body)
			resp, err := client.Post(server.URL+tc.route, "application/json", bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			text, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(text), "symbolic link") {
				t.Fatalf("status %d: %s", resp.StatusCode, text)
			}
			entries, err := os.ReadDir(filepath.Join(root, "Journal"))
			if err != nil || len(entries) != 1 {
				t.Fatalf("Journal holds %v (%v)", entries, err)
			}
		})
	}
}

// A client showing part of a vault needs to know the marks can be trusted: on a
// server without them, a link reads like a real note.
func TestCapabilitiesAdvertiseSymlinkMarks(t *testing.T) {
	root := t.TempDir()
	server, _ := newTestServer(t, config.Config{
		VaultPath:        root,
		DefaultVaultPath: root,
		Bind:             "127.0.0.1:7878",
		AuthToken:        "secret-token",
		BrowseRoots:      []string{root},
	})
	resp, err := http.Get(server.URL + "/api/capabilities")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var caps map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&caps); err != nil {
		t.Fatal(err)
	}
	if caps["supportsSymlinkMarks"] != true {
		t.Errorf("capabilities do not advertise supportsSymlinkMarks: %v", caps)
	}
}

// The folder names come only with a token, and capabilities need none.
func TestLinkedSystemFoldersNeedAToken(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Journal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("Journal", filepath.Join(root, "quick")); err != nil {
		t.Fatal(err)
	}
	server, _ := newTestServer(t, config.Config{
		VaultPath:        root,
		DefaultVaultPath: root,
		Bind:             "127.0.0.1:7878",
		AuthToken:        "secret-token",
		BrowseRoots:      []string{root},
	})
	anonymous, err := http.Get(server.URL + "/api/vault")
	if err != nil {
		t.Fatal(err)
	}
	anonymous.Body.Close()
	if anonymous.StatusCode != http.StatusUnauthorized {
		t.Errorf("without a token: status %d", anonymous.StatusCode)
	}
	public, err := http.Get(server.URL + "/api/capabilities")
	if err != nil {
		t.Fatal(err)
	}
	defer public.Body.Close()
	if text, _ := io.ReadAll(public.Body); strings.Contains(string(text), "quick") {
		t.Errorf("capabilities name a folder: %s", text)
	}
	client := &http.Client{Jar: loginAndJar(t, server, "secret-token")}
	resp, err := client.Get(server.URL + "/api/vault")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var info struct {
		LinkedSystemFolders []string `json:"linkedSystemFolders"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	if len(info.LinkedSystemFolders) != 1 || info.LinkedSystemFolders[0] != "quick" {
		t.Errorf("got %v", info.LinkedSystemFolders)
	}
}

// "None" has to read differently from a server that does not report this.
func TestVaultInfoAlwaysSendsLinkedSystemFolders(t *testing.T) {
	root := t.TempDir()
	server, _ := newTestServer(t, config.Config{
		VaultPath:        root,
		DefaultVaultPath: root,
		Bind:             "127.0.0.1:7878",
		AuthToken:        "secret-token",
		BrowseRoots:      []string{root},
	})
	client := &http.Client{Jar: loginAndJar(t, server, "secret-token")}
	resp, err := client.Get(server.URL + "/api/vault")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	text, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), `"linkedSystemFolders":[]`) {
		t.Errorf("got %s", text)
	}
}
