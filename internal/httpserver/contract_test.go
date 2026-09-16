package httpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"reflect"
	"testing"

	"github.com/ZenNotes/zennotes/apps/server/internal/config"
)

func TestSelfHostedHTTPContract(t *testing.T) {
	data, err := os.ReadFile("testdata/self-hosted-http.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		SchemaVersion int
		Protocol      string
		MountPaths    []string
		RoutePrefixes []string
		Note          struct {
			Path, Body, UpdatedBody         string
			AssetEmbeds, UpdatedAssetEmbeds []string
		}
		RequiredNoteFields, RequiredCapabilities []string
		Errors                                   struct {
			Unauthenticated, MissingNote, DirectoryAsNote int
			Challenge                                     string
		}
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.SchemaVersion != 1 || fixture.Protocol != "self-hosted-http-v1" {
		t.Fatal("unsupported fixture")
	}
	for _, base := range fixture.MountPaths {
		for _, prefix := range fixture.RoutePrefixes {
			t.Run("mount="+base+", api="+prefix, func(t *testing.T) {
				root := t.TempDir()
				const token = "test-only-contract-token"
				server, v := newTestServer(t, config.Config{VaultPath: root, DefaultVaultPath: root, BasePath: base, AuthToken: token, BrowseRoots: []string{root}})
				if _, err := v.WriteNote(fixture.Note.Path, fixture.Note.Body); err != nil {
					t.Fatal(err)
				}
				endpoint := server.URL + base + prefix
				request := func(client *http.Client, method, path string, body any, bearer bool) *http.Response {
					t.Helper()
					var input io.Reader
					if body != nil {
						encoded, err := json.Marshal(body)
						if err != nil {
							t.Fatal(err)
						}
						input = bytes.NewReader(encoded)
					}
					req, err := http.NewRequest(method, endpoint+path, input)
					if err != nil {
						t.Fatal(err)
					}
					if body != nil {
						req.Header.Set("Content-Type", "application/json")
					}
					if bearer {
						req.Header.Set("Authorization", "Bearer "+token)
					}
					response, err := client.Do(req)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { response.Body.Close() })
					return response
				}
				decode := func(response *http.Response, status int) map[string]json.RawMessage {
					t.Helper()
					if response.StatusCode != status {
						body, _ := io.ReadAll(response.Body)
						t.Fatalf("status %d, want %d: %s", response.StatusCode, status, body)
					}
					var value map[string]json.RawMessage
					if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
						t.Fatal(err)
					}
					return value
				}
				assertNote := func(value map[string]json.RawMessage, embeds []string, body *string) {
					t.Helper()
					for _, field := range fixture.RequiredNoteFields {
						if _, ok := value[field]; !ok {
							t.Errorf("missing required note field: %s", field)
						}
					}
					var assets []string
					if err := json.Unmarshal(value["assetEmbeds"], &assets); err != nil {
						t.Error(err)
					} else if !reflect.DeepEqual(assets, embeds) {
						t.Errorf("assetEmbeds = %#v, want %#v", assets, embeds)
					}
					if body != nil {
						var actual string
						if err := json.Unmarshal(value["body"], &actual); err != nil {
							t.Fatal(err)
						}
						if actual != *body {
							t.Fatal("HTTP response changed Markdown bytes")
						}
					}
				}
				caps := decode(request(http.DefaultClient, "GET", "/capabilities", nil, false), 200)
				for _, field := range fixture.RequiredCapabilities {
					if _, ok := caps[field]; !ok {
						t.Errorf("missing capability: %s", field)
					}
				}
				readPath := "/notes/read?path=" + url.QueryEscape(fixture.Note.Path)
				anonymous := request(http.DefaultClient, "GET", readPath, nil, false)
				if anonymous.StatusCode != fixture.Errors.Unauthenticated || anonymous.Header.Get("WWW-Authenticate") != fixture.Errors.Challenge {
					t.Fatalf("anonymous response: status=%d, challenge=%q", anonymous.StatusCode, anonymous.Header.Get("WWW-Authenticate"))
				}
				assertNote(decode(request(http.DefaultClient, "GET", readPath, nil, true), 200), fixture.Note.AssetEmbeds, &fixture.Note.Body)
				update := map[string]string{"path": fixture.Note.Path, "body": fixture.Note.UpdatedBody}
				assertNote(decode(request(http.DefaultClient, "POST", "/notes/write", update, true), 200), fixture.Note.UpdatedAssetEmbeds, nil)
				assertNote(decode(request(http.DefaultClient, "GET", readPath, nil, true), 200), fixture.Note.UpdatedAssetEmbeds, &fixture.Note.UpdatedBody)
				stored, err := v.ReadNote(fixture.Note.Path)
				if err != nil || stored.Body != fixture.Note.UpdatedBody {
					t.Fatalf("stored bytes differ: %v", err)
				}
				for path, status := range map[string]int{"missing.md": fixture.Errors.MissingNote, "inbox": fixture.Errors.DirectoryAsNote} {
					if response := request(http.DefaultClient, "GET", "/notes/read?path="+url.QueryEscape(path), nil, true); response.StatusCode != status {
						t.Errorf("read %s: got %d, want %d", path, response.StatusCode, status)
					}
				}
				jar, err := cookiejar.New(nil)
				if err != nil {
					t.Fatal(err)
				}
				client := &http.Client{Jar: jar}
				apiURL, _ := url.Parse(server.URL + base + "/api/session")
				jar.SetCookies(apiURL, []*http.Cookie{{Name: sessionCookieName, Value: "old-narrow-cookie", Path: base + "/api"}})
				login := request(client, "POST", "/session/login", map[string]string{"token": token}, false)
				decode(login, 200)
				validCookie := false
				for _, cookie := range login.Cookies() {
					if cookie.Name == sessionCookieName && cookie.Value != "" && cookie.Path == base+"/" {
						validCookie = true
					}
				}
				if !validCookie {
					t.Errorf("session cookie does not cover mount %q", base)
				}
				if cookies := jar.Cookies(apiURL); len(cookies) != 1 || cookies[0].Value == "old-narrow-cookie" {
					t.Fatal("login did not replace the old API cookie")
				}

				assertNote(decode(request(client, "GET", readPath, nil, false), 200), fixture.Note.UpdatedAssetEmbeds, &fixture.Note.UpdatedBody)
				// A cached client can change route families after a browser upgrade.
				if prefix == "/api" {
					endpoint = server.URL + base
				} else {
					endpoint = server.URL + base + "/api"
				}
				assertNote(decode(request(client, "GET", readPath, nil, false), 200), fixture.Note.UpdatedAssetEmbeds, &fixture.Note.UpdatedBody)
				decode(request(client, "POST", "/session/logout", nil, false), 200)
				if len(jar.Cookies(apiURL)) != 0 {
					t.Fatal("logout left a session cookie")
				}
				if response := request(client, "GET", readPath, nil, false); response.StatusCode != 401 {
					t.Fatal("logout left an authenticated session")
				}
			})
		}
	}
}
