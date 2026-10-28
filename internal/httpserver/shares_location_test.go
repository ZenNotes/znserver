package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/ZenNotes/znserver/internal/config"
)

// A share to a server vault (2.79) is filed by the desktop app: it reads
// sharesLocation from GET /vault/settings, resolves it with shared-domain's
// resolveShareLocation, and creates the note through /notes/create. Before the
// server kept the field, a settings save dropped it and every share landed in
// Quick Notes whatever the vault chose. This drives that whole path over HTTP,
// saving settings the way the desktop does: the full object it read, changed.
func TestSharesLocationRoundTripsAndFilesOverHTTP(t *testing.T) {
	root := t.TempDir()
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
	// resolveShareLocation, as the desktop runs it on what it read back.
	resolve := func(raw json.RawMessage) (folder, subpath string) {
		var setting struct{ Mode, Folder string }
		if err := json.Unmarshal(raw, &setting); err != nil {
			t.Fatal(err)
		}
		switch {
		case setting.Mode == "quick":
			return "quick", ""
		case setting.Mode == "folder" && setting.Folder != "":
			return "inbox", setting.Folder
		default:
			return "inbox", ""
		}
	}

	var caps struct {
		SupportsSharesLocation bool `json:"supportsSharesLocation"`
	}
	if err := json.Unmarshal(send("GET", "/api/capabilities", nil), &caps); err != nil {
		t.Fatal(err)
	}
	if !caps.SupportsSharesLocation {
		t.Error("capabilities do not advertise supportsSharesLocation")
	}

	if got := string(fields(send("GET", "/api/vault/settings", nil))["sharesLocation"]); got != `{"mode":"quick"}` {
		t.Errorf("default sharesLocation = %s, want Quick Notes", got)
	}

	cases := []struct {
		title, setting, stored, path string
	}{
		{"Quick share", `{"mode":"quick"}`, `{"mode":"quick"}`, "quick/Quick share.md"},
		{"Primary share", `{"mode":"primary"}`, `{"mode":"primary"}`, "inbox/Primary share.md"},
		{"Folder share", `{"mode":"folder","folder":" /Clippings/ "}`, `{"mode":"folder","folder":"Clippings"}`, "inbox/Clippings/Folder share.md"},
		{"Nested share", `{"mode":"folder","folder":"Web/Clippings"}`, `{"mode":"folder","folder":"Web/Clippings"}`, "inbox/Web/Clippings/Nested share.md"},
		// Folder mode with no folder yet files like primary.
		{"Unnamed share", `{"mode":"folder","folder":""}`, `{"mode":"folder","folder":""}`, "inbox/Unnamed share.md"},
		// Kept as typed, as the desktop keeps it; /notes/create's SafeJoin
		// holds it inside the primary notes location.
		{"Escape share", `{"mode":"folder","folder":"../Escape"}`, `{"mode":"folder","folder":"../Escape"}`, "inbox/Escape/Escape share.md"},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			next := fields(send("GET", "/api/vault/settings", nil))
			next["sharesLocation"] = json.RawMessage(tc.setting)
			saved := send("POST", "/api/vault/settings", next)
			if got := string(fields(saved)["sharesLocation"]); got != tc.stored {
				t.Errorf("POST answered sharesLocation %s, want %s", got, tc.stored)
			}
			read := send("GET", "/api/vault/settings", nil)
			var savedValue, readValue any
			if err := json.Unmarshal(saved, &savedValue); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(read, &readValue); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(savedValue, readValue) {
				t.Errorf("GET differs from what POST stored:\nPOST %s\nGET  %s", saved, read)
			}

			folder, subpath := resolve(fields(read)["sharesLocation"])
			var meta struct {
				Path string `json:"path"`
			}
			created := send("POST", "/api/notes/create", map[string]string{"folder": folder, "title": tc.title, "subpath": subpath})
			if err := json.Unmarshal(created, &meta); err != nil {
				t.Fatal(err)
			}
			if meta.Path != tc.path {
				t.Errorf("share filed at %q, want %q", meta.Path, tc.path)
			}
		})
	}
}
