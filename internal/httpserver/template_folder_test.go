package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/ZenNotes/znserver/internal/config"
)

// The template folder (#1024) over HTTP, the way a desktop on a server vault
// or the web client uses it: the capability says the server keeps it, a
// settings save of the full object the client read keeps it, and the task
// list and text search leave that folder's notes out. The clients read the
// templates themselves through the note routes, which needed no change.
func TestTemplateFolderOverHTTP(t *testing.T) {
	root := t.TempDir()
	for rel, body := range map[string]string{
		".zennotes/vault.json": `{"primaryNotesLocation":"root"}`,
		"Templates/Meeting.md": "# {{title}}\n\n- [ ] zqxagenda item\n",
		"Notes/Plan.md":        "- [ ] zqxagenda real work\n",
	} {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	server, _ := newTestServer(t, config.Config{
		VaultPath:        root,
		DefaultVaultPath: root,
		Bind:             "127.0.0.1:7878",
		AuthToken:        "secret-token",
		BrowseRoots:      []string{root},
	})
	client := &http.Client{Jar: loginAndJar(t, server, "secret-token")}

	send := func(method, path string, payload any) []byte {
		t.Helper()
		var body bytes.Buffer
		if payload != nil {
			if err := json.NewEncoder(&body).Encode(payload); err != nil {
				t.Fatal(err)
			}
		}
		req, err := http.NewRequest(method, server.URL+path, &body)
		if err != nil {
			t.Fatal(err)
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		var out bytes.Buffer
		if _, err := out.ReadFrom(resp.Body); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s status = %d: %s", method, path, resp.StatusCode, out.String())
		}
		return out.Bytes()
	}
	fields := func(raw []byte) map[string]json.RawMessage {
		t.Helper()
		var out map[string]json.RawMessage
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		return out
	}
	sources := func(raw []byte, key string) map[string]bool {
		t.Helper()
		var rows []map[string]any
		if err := json.Unmarshal(raw, &rows); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		out := map[string]bool{}
		for _, row := range rows {
			if path, ok := row[key].(string); ok {
				out[path] = true
			}
		}
		return out
	}

	var caps struct {
		SupportsTemplateFolder bool `json:"supportsTemplateFolder"`
	}
	if err := json.Unmarshal(send("GET", "/api/capabilities", nil), &caps); err != nil {
		t.Fatal(err)
	}
	if !caps.SupportsTemplateFolder {
		t.Error("capabilities do not advertise supportsTemplateFolder")
	}

	if got := sources(send("GET", "/api/tasks", nil), "sourcePath"); !got["Templates/Meeting.md"] {
		t.Fatalf("without a template folder the template's tasks are listed, got %v", got)
	}

	next := fields(send("GET", "/api/vault/settings", nil))
	next["templates"] = json.RawMessage(`{"folder":" Templates/ "}`)
	if got := string(fields(send("POST", "/api/vault/settings", next))["templates"]); got != `{"folder":"Templates"}` {
		t.Errorf("POST answered templates %s", got)
	}
	if got := string(fields(send("GET", "/api/vault/settings", nil))["templates"]); got != `{"folder":"Templates"}` {
		t.Errorf("GET answered templates %s", got)
	}

	tasks := sources(send("GET", "/api/tasks", nil), "sourcePath")
	if tasks["Templates/Meeting.md"] || !tasks["Notes/Plan.md"] {
		t.Errorf("tasks with a template folder = %v", tasks)
	}
	hits := sources(send("GET", "/api/search/text?q="+url.QueryEscape("zqxagenda"), nil), "path")
	if hits["Templates/Meeting.md"] || !hits["Notes/Plan.md"] {
		t.Errorf("text search with a template folder = %v", hits)
	}
}
