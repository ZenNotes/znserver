package httpserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZenNotes/znserver/internal/config"
)

// Conditional note writes (#1108), the way a client that lost the server and
// came back uses them: it read the note and kept its sha256, and its save
// sends that back so the server refuses it if someone else saved the note in
// between, instead of writing over them.
func TestConditionalNoteWriteOverHTTP(t *testing.T) {
	root := t.TempDir()
	note := filepath.Join(root, "inbox", "Plan.md")
	if err := os.MkdirAll(filepath.Dir(note), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(note, []byte("first\n"), 0o644); err != nil {
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

	send := func(method, path string, payload any) (int, []byte) {
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
		return resp.StatusCode, out.Bytes()
	}
	hashOf := func(raw []byte) string {
		t.Helper()
		var out struct {
			Sha256 string `json:"sha256"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		return out.Sha256
	}
	sum := func(text string) string {
		s := sha256.Sum256([]byte(text))
		return hex.EncodeToString(s[:])
	}
	disk := func() string {
		t.Helper()
		data, err := os.ReadFile(note)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	var caps struct {
		SupportsConditionalNoteWrites bool `json:"supportsConditionalNoteWrites"`
	}
	if _, raw := send("GET", "/api/capabilities", nil); json.Unmarshal(raw, &caps) != nil || !caps.SupportsConditionalNoteWrites {
		t.Fatalf("capabilities do not advertise supportsConditionalNoteWrites: %s", raw)
	}

	// A read names the bytes on disk.
	status, raw := send("GET", "/api/notes/read?path=inbox/Plan.md", nil)
	if status != http.StatusOK {
		t.Fatalf("read status = %d: %s", status, raw)
	}
	base := hashOf(raw)
	if base != sum("first\n") {
		t.Fatalf("read sha256 = %q, want the hash of the file", base)
	}

	// A save against what was read lands, and names what it wrote.
	status, raw = send("POST", "/api/notes/write", map[string]string{
		"path": "inbox/Plan.md", "body": "second\n", "baseSha256": base,
	})
	if status != http.StatusOK {
		t.Fatalf("conditional write status = %d: %s", status, raw)
	}
	next := hashOf(raw)
	if next != sum("second\n") || disk() != "second\n" {
		t.Fatalf("write sha256 = %q, disk %q", next, disk())
	}

	// Someone else saves (here, straight to disk, the way an editor on the
	// server or `git pull` would); a save still built on the old version is
	// refused and leaves their text alone.
	if err := os.WriteFile(note, []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, raw = send("POST", "/api/notes/write", map[string]string{
		"path": "inbox/Plan.md", "body": "mine\n", "baseSha256": next,
	})
	if status != http.StatusConflict {
		t.Fatalf("stale write status = %d (%s), want 409", status, raw)
	}
	if disk() != "theirs\n" {
		t.Fatalf("a refused write changed the note: %q", disk())
	}

	// A write without a base is what every older client sends: unconditional,
	// exactly as before.
	status, raw = send("POST", "/api/notes/write", map[string]string{
		"path": "inbox/Plan.md", "body": "mine\n",
	})
	if status != http.StatusOK || disk() != "mine\n" {
		t.Fatalf("unconditional write status = %d (%s), disk %q", status, raw, disk())
	}

	// A note deleted since it was read is a change too: recreating it is the
	// client's call, made by writing again without a base.
	if err := os.Remove(note); err != nil {
		t.Fatal(err)
	}
	status, raw = send("POST", "/api/notes/write", map[string]string{
		"path": "inbox/Plan.md", "body": "mine\n", "baseSha256": sum("mine\n"),
	})
	if status != http.StatusConflict {
		t.Fatalf("write over a deleted note status = %d (%s), want 409", status, raw)
	}
	if _, err := os.Stat(note); !os.IsNotExist(err) {
		t.Fatalf("a refused write recreated the note: %v", err)
	}

	// The base is compared as hex, whatever its case.
	if err := os.WriteFile(note, []byte("again\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, raw = send("POST", "/api/notes/write", map[string]string{
		"path": "inbox/Plan.md", "body": "done\n", "baseSha256": strings.ToUpper(sum("again\n")),
	})
	if status != http.StatusOK || disk() != "done\n" {
		t.Fatalf("upper-case base status = %d (%s), disk %q", status, raw, disk())
	}

	// A base that is not a SHA-256 is refused outright, never compared and
	// never dropped into an unconditional write.
	status, raw = send("POST", "/api/notes/write", map[string]string{
		"path": "inbox/Plan.md", "body": "garbage base\n", "baseSha256": "not-a-hash",
	})
	if status != http.StatusBadRequest || disk() != "done\n" {
		t.Fatalf("malformed base status = %d (%s), disk %q", status, raw, disk())
	}

	// Listings stay cheap: no hash per note.
	if _, raw := send("GET", "/api/notes", nil); strings.Contains(string(raw), `"sha256"`) {
		t.Fatalf("the note listing carries hashes: %s", raw)
	}
}
