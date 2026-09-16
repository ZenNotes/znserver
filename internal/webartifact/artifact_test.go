package webartifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func dirty(value bool) *bool { return &value }

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type entry struct {
	name, body string
	typeflag   byte
}

func candidate(t *testing.T, entries []entry, change func(*Manifest)) (string, string) {
	t.Helper()
	root := t.TempDir()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	for _, item := range entries {
		kind := item.typeflag
		if kind == 0 {
			kind = tar.TypeReg
		}
		header := &tar.Header{Name: item.name, Mode: 0o644, Typeflag: kind, Size: int64(len(item.body))}
		if kind == tar.TypeSymlink || kind == tar.TypeLink {
			header.Linkname = "../../outside"
			header.Size = 0
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := tw.Write([]byte(item.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		SchemaVersion: 1, Artifact: "zennotes-self-hosted-web", Version: "1.0.0-test",
		Protocol:    "self-hosted-http-v1",
		Source:      Source{Repository: "https://github.com/ZenNotes/zennotes", Commit: strings.Repeat("a", 40), Dirty: dirty(false)},
		Archive:     Archive{File: "web.tgz", Size: int64(buffer.Len()), SHA256: digest(buffer.Bytes())},
		Entrypoints: []string{"index.html"},
		Files:       []File{{Path: "index.html", Size: 5, SHA256: digest([]byte("hello"))}},
	}
	if change != nil {
		change(&manifest)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "web.json")
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "web.tgz"), buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return manifestPath, filepath.Join(root, "build", "dist")
}

func TestInstallVerifiedArtifactAndReuse(t *testing.T) {
	manifest, target := candidate(t, []entry{{name: "package/dist/index.html", body: "hello"}}, nil)
	for range 2 {
		if err := Install(context.Background(), manifest, "", target, false); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(target, "index.html"))
	if err != nil || string(data) != "hello" {
		t.Fatalf("installed entrypoint: %q, %v", data, err)
	}
}

func TestInvalidArtifactsLeaveDestinationAbsent(t *testing.T) {
	valid := []entry{{name: "package/dist/index.html", body: "hello"}}
	tests := []struct {
		name    string
		entries []entry
		change  func(*Manifest)
	}{
		{"archive checksum", valid, func(m *Manifest) { m.Archive.SHA256 = strings.Repeat("0", 64) }},
		{"file checksum", valid, func(m *Manifest) { m.Files[0].SHA256 = strings.Repeat("0", 64) }},
		{"wrong size", valid, func(m *Manifest) { m.Files[0].Size++ }},
		{"missing entrypoint", nil, nil},
		{"unexpected file", append(append([]entry{}, valid...), entry{name: "package/dist/extra.js", body: "extra"}), nil},
		{"duplicate file", append(append([]entry{}, valid...), valid...), nil},
		{"traversal", []entry{{name: "package/dist/../../outside", body: "bad"}}, nil},
		{"absolute path", []entry{{name: "/outside", body: "bad"}}, nil},
		{"backslash", []entry{{name: `package/dist/..\outside`, body: "bad"}}, nil},
		{"symlink", []entry{{name: "package/dist/index.html", typeflag: tar.TypeSymlink}}, nil},
		{"hard link", []entry{{name: "package/dist/index.html", typeflag: tar.TypeLink}}, nil},
		{"dirty source", valid, func(m *Manifest) { m.Source.Dirty = dirty(true) }},
		{"unknown protocol", valid, func(m *Manifest) { m.Protocol = "cloud-v1" }},
		{"unsafe manifest path", valid, func(m *Manifest) { m.Files[0].Path = "../outside" }},
		{"case collision", valid, func(m *Manifest) { f := m.Files[0]; f.Path = "INDEX.html"; m.Files = append(m.Files, f) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			manifest, target := candidate(t, tc.entries, tc.change)
			if err := Install(context.Background(), manifest, "", target, false); err == nil {
				t.Fatal("accepted invalid artifact")
			}
			if _, err := os.Lstat(target); !os.IsNotExist(err) {
				t.Fatalf("failed install left destination behind: %v", err)
			}
		})
	}
}

func TestExistingBuildIsNeverReplaced(t *testing.T) {
	manifest, target := candidate(t, []entry{{name: "package/dist/index.html", body: "hello"}}, nil)
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "index.html"), []byte("previous build"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Install(context.Background(), manifest, "", target, false); err == nil {
		t.Fatal("replaced an existing distribution")
	}
	data, _ := os.ReadFile(filepath.Join(target, "index.html"))
	if string(data) != "previous build" {
		t.Fatal("changed previous distribution bytes")
	}
}

func TestDirtyCandidateRequiresExplicitOptIn(t *testing.T) {
	manifest, target := candidate(t, []entry{{name: "package/dist/index.html", body: "hello"}}, func(m *Manifest) { m.Source.Dirty = dirty(true) })
	if err := Install(context.Background(), manifest, "", target, true); err != nil {
		t.Fatal(err)
	}
}

func TestManifestRequiresUnambiguousSchema(t *testing.T) {
	for _, tc := range []struct{ name, from, to string }{
		{"missing dirty", `"dirty":false`, `"lockfileSha256":""`},
		{"null dirty", `"dirty":false`, `"dirty":null`},
		{"unknown nested field", `"dirty":false`, `"dirty":false,"reviewed":true`},
		{"unknown top field", `"schemaVersion":1`, `"schemaVersion":1,"verified":true`},
		{"duplicate dirty", `"dirty":false`, `"dirty":true,"dirty":false`},
		{"case duplicate dirty", `"dirty":false`, `"Dirty":true,"dirty":false`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest, _ := candidate(t, []entry{{name: "package/dist/index.html", body: "hello"}}, nil)
			data, err := os.ReadFile(manifest)
			if err != nil {
				t.Fatal(err)
			}
			changed := strings.Replace(string(data), tc.from, tc.to, 1)
			if changed == string(data) {
				t.Fatal("test did not change manifest")
			}
			if err := os.WriteFile(manifest, []byte(changed), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadManifest(manifest, false); err == nil {
				t.Fatal("accepted ambiguous manifest")
			}
		})
	}
}

func TestExistingTreeRejectsExtraFilesAndLinks(t *testing.T) {
	for _, link := range []bool{false, true} {
		t.Run(fmt.Sprint(link), func(t *testing.T) {
			manifest, target := candidate(t, []entry{{name: "package/dist/index.html", body: "hello"}}, nil)
			if err := Install(context.Background(), manifest, "", target, false); err != nil {
				t.Fatal(err)
			}
			extra := filepath.Join(target, "extra")
			var err error
			if link {
				err = os.Symlink("index.html", extra)
			} else {
				err = os.WriteFile(extra, []byte("extra"), 0o600)
			}
			if err != nil {
				if link {
					t.Skipf("symlink unavailable: %v", err)
				}
				t.Fatal(err)
			}
			if err := Install(context.Background(), manifest, "", target, false); err == nil {
				t.Fatal("reused unexpected file")
			}
			if _, err := os.Lstat(extra); err != nil {
				t.Fatal("changed existing tree")
			}
		})
	}
}

func TestInstallLockAndArchiveOverride(t *testing.T) {
	manifest, target := candidate(t, []entry{{name: "package/dist/index.html", body: "hello"}}, nil)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	lock := target + ".install-lock"
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Install(context.Background(), manifest, "", target, false); err == nil {
		t.Fatal("ignored another installer")
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	override := filepath.Join(filepath.Dir(manifest), "candidate with spaces & symbols.tgz")
	if err := os.Rename(filepath.Join(filepath.Dir(manifest), "web.tgz"), override); err != nil {
		t.Fatal(err)
	}
	if err := Install(context.Background(), manifest, override, target, false); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPSDownloads(t *testing.T) {
	for _, mode := range []string{"success", "http", "downgrade", "redirect loop", "credentials", "oversized", "wrong checksum"} {
		t.Run(mode, func(t *testing.T) {
			var payload []byte
			var serverURL string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Error("download sent credentials")
				}
				switch mode {
				case "downgrade":
					http.Redirect(w, r, "http://127.0.0.1:1/forbidden", http.StatusFound)
				case "redirect loop":
					http.Redirect(w, r, serverURL+"/again", http.StatusFound)
				case "oversized":
					w.Write(append(payload, 'x'))
				case "wrong checksum":
					w.Write(bytes.Repeat([]byte{'x'}, len(payload)))
				default:
					w.Write(payload)
				}
			}))
			defer server.Close()
			serverURL = server.URL
			// Trust only this test server's certificate without changing production TLS.
			previous := http.DefaultTransport
			http.DefaultTransport = server.Client().Transport
			defer func() { http.DefaultTransport = previous }()
			download := server.URL + "/web.tgz"
			if mode == "http" {
				download = strings.Replace(download, "https:", "http:", 1)
			}
			if mode == "credentials" {
				download = strings.Replace(download, "https://", "https://user:secret@", 1)
			}
			manifest, target := candidate(t, []entry{{name: "package/dist/index.html", body: "hello"}}, func(m *Manifest) { m.Archive.URL = download })
			archive := filepath.Join(filepath.Dir(manifest), "web.tgz")
			var err error
			payload, err = os.ReadFile(archive)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(archive); err != nil {
				t.Fatal(err)
			}
			err = Install(context.Background(), manifest, "", target, false)
			if mode == "success" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil {
					t.Fatal("accepted invalid download")
				}
				if _, err := os.Lstat(target); !os.IsNotExist(err) {
					t.Fatal("failed download exposed files")
				}
			}
		})
	}
}

func TestManifestRejectsTrailingData(t *testing.T) {
	manifest, _ := candidate(t, []entry{{name: "package/dist/index.html", body: "hello"}}, nil)
	f, err := os.OpenFile(manifest, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.WriteString(f, "{}")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(manifest, false); err == nil {
		t.Fatal("accepted trailing JSON")
	}
}
