package vault

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every case shared-domain's normalizeShareLocation decides, on the value
// after decoding. The expected results are what the TypeScript function
// returns for the same input.
func TestNormalizeShareLocation(t *testing.T) {
	cases := []struct {
		name string
		in   ShareLocationSetting
		want ShareLocationSetting
	}{
		{"absent is Quick Notes", ShareLocationSetting{}, ShareLocationSetting{Mode: ShareLocationQuick}},
		{"quick", ShareLocationSetting{Mode: ShareLocationQuick}, ShareLocationSetting{Mode: ShareLocationQuick}},
		{"quick drops a folder", ShareLocationSetting{Mode: ShareLocationQuick, Folder: "Clippings"}, ShareLocationSetting{Mode: ShareLocationQuick}},
		{"primary", ShareLocationSetting{Mode: ShareLocationPrimary}, ShareLocationSetting{Mode: ShareLocationPrimary}},
		{"primary drops a folder", ShareLocationSetting{Mode: ShareLocationPrimary, Folder: "Clippings"}, ShareLocationSetting{Mode: ShareLocationPrimary}},
		{"folder", ShareLocationSetting{Mode: ShareLocationFolder, Folder: "Clippings"}, ShareLocationSetting{Mode: ShareLocationFolder, Folder: "Clippings"}},
		{"nested folder", ShareLocationSetting{Mode: ShareLocationFolder, Folder: "Web/Clippings"}, ShareLocationSetting{Mode: ShareLocationFolder, Folder: "Web/Clippings"}},
		{"empty folder keeps folder mode", ShareLocationSetting{Mode: ShareLocationFolder}, ShareLocationSetting{Mode: ShareLocationFolder}},
		{"whitespace-only folder is empty", ShareLocationSetting{Mode: ShareLocationFolder, Folder: " \t\n "}, ShareLocationSetting{Mode: ShareLocationFolder}},
		{"slashes-only folder is empty", ShareLocationSetting{Mode: ShareLocationFolder, Folder: "///"}, ShareLocationSetting{Mode: ShareLocationFolder}},
		{"surrounding whitespace and slashes", ShareLocationSetting{Mode: ShareLocationFolder, Folder: " /Clippings/ "}, ShareLocationSetting{Mode: ShareLocationFolder, Folder: "Clippings"}},
		{"runs of slashes", ShareLocationSetting{Mode: ShareLocationFolder, Folder: "//Web/Clippings//"}, ShareLocationSetting{Mode: ShareLocationFolder, Folder: "Web/Clippings"}},
		{"absolute path loses its leading slash", ShareLocationSetting{Mode: ShareLocationFolder, Folder: "/Users/me/Clippings"}, ShareLocationSetting{Mode: ShareLocationFolder, Folder: "Users/me/Clippings"}},
		// Trimmed once, before the slashes: whitespace inside them stays.
		{"whitespace inside the slashes stays", ShareLocationSetting{Mode: ShareLocationFolder, Folder: " / Clippings / "}, ShareLocationSetting{Mode: ShareLocationFolder, Folder: " Clippings "}},
		{"inner double slash stays", ShareLocationSetting{Mode: ShareLocationFolder, Folder: "Web//Clippings"}, ShareLocationSetting{Mode: ShareLocationFolder, Folder: "Web//Clippings"}},
		{"backslashes stay", ShareLocationSetting{Mode: ShareLocationFolder, Folder: `Web\Clippings\`}, ShareLocationSetting{Mode: ShareLocationFolder, Folder: `Web\Clippings\`}},
		// Traversal is CreateNote's to refuse (SafeJoin), on both runtimes.
		{"traversal is kept for CreateNote", ShareLocationSetting{Mode: ShareLocationFolder, Folder: "../Escape"}, ShareLocationSetting{Mode: ShareLocationFolder, Folder: "../Escape"}},
		// JavaScript's trim(), not Go's TrimSpace: NBSP, BOM and the Unicode
		// separators go; NEL (U+0085) is not JavaScript whitespace and stays.
		{"JS whitespace", ShareLocationSetting{Mode: ShareLocationFolder, Folder: "\u00a0\u2028Clippings\u3000\ufeff"}, ShareLocationSetting{Mode: ShareLocationFolder, Folder: "Clippings"}},
		{"NEL is not JS whitespace", ShareLocationSetting{Mode: ShareLocationFolder, Folder: "\u0085Clippings"}, ShareLocationSetting{Mode: ShareLocationFolder, Folder: "\u0085Clippings"}},
		{"active-note is not a share mode", ShareLocationSetting{Mode: "active-note"}, ShareLocationSetting{Mode: ShareLocationQuick}},
		{"modes are case-sensitive", ShareLocationSetting{Mode: "Folder", Folder: "Clippings"}, ShareLocationSetting{Mode: ShareLocationQuick}},
		{"unknown mode", ShareLocationSetting{Mode: "bogus", Folder: "Clippings"}, ShareLocationSetting{Mode: ShareLocationQuick}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeShareLocation(tc.in); got != tc.want {
				t.Errorf("normalizeShareLocation(%+v) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

// What vault.json can hold, decoded and normalized and written back, against
// JSON.stringify(normalizeShareLocation(JSON.parse(raw))). A value the
// TypeScript side reads as absent must not fail the whole file here.
func TestShareLocationJSONMatchesTypeScript(t *testing.T) {
	quick := `{"mode":"quick"}`
	emptyFolder := `{"mode":"folder","folder":""}`
	cases := []struct{ raw, want string }{
		{`null`, quick},
		{`"folder"`, quick},
		{`5`, quick},
		{`true`, quick},
		{`[]`, quick},
		{`["folder","Clippings"]`, quick},
		{`{}`, quick},
		{`{"mode":null}`, quick},
		{`{"mode":5}`, quick},
		{`{"mode":["folder"]}`, quick},
		{`{"mode":"quick"}`, quick},
		{`{"mode":"quick","folder":"Clippings"}`, quick},
		{`{"mode":"primary"}`, `{"mode":"primary"}`},
		{`{"mode":"primary","folder":"Clippings"}`, `{"mode":"primary"}`},
		{`{"mode":"folder"}`, emptyFolder},
		{`{"mode":"folder","folder":null}`, emptyFolder},
		{`{"mode":"folder","folder":7}`, emptyFolder},
		{`{"mode":"folder","folder":{"name":"Clippings"}}`, emptyFolder},
		{`{"mode":"folder","folder":""}`, emptyFolder},
		{`{"mode":"folder","folder":" /Clippings/ "}`, `{"mode":"folder","folder":"Clippings"}`},
		{`{"mode":"folder","folder":"Web/Clippings","extra":true}`, `{"mode":"folder","folder":"Web/Clippings"}`},
		// A property read is exact, so `Mode` is not `mode`.
		{`{"Mode":"primary"}`, quick},
		{`{"mode":"folder","Folder":"Clippings"}`, emptyFolder},
		// JSON.parse keeps the last of a repeated key.
		{`{"mode":"quick","mode":"primary"}`, `{"mode":"primary"}`},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			var decoded ShareLocationSetting
			if err := json.Unmarshal([]byte(tc.raw), &decoded); err != nil {
				t.Fatalf("decode %s: %v", tc.raw, err)
			}
			out, err := json.Marshal(normalizeShareLocation(decoded))
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != tc.want {
				t.Errorf("%s -> %s, want %s", tc.raw, out, tc.want)
			}
		})
	}
}

// A self-hosted vault keeps where its shares go: the setting survives
// SetSettings, reaches vault.json, and comes back from GetSettings, including
// after a cache-cold reparse of the file the desktop app may have written.
func TestVaultSettingsSharesLocationRoundTrip(t *testing.T) {
	root := t.TempDir()
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}

	got, err := v.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.SharesLocation != (ShareLocationSetting{Mode: ShareLocationQuick}) {
		t.Errorf("default shares location = %+v, want quick", got.SharesLocation)
	}

	saved, err := v.SetSettings(VaultSettings{
		PrimaryNotesLocation: PrimaryNotesInbox,
		SharesLocation:       ShareLocationSetting{Mode: ShareLocationFolder, Folder: " /Clippings/ "},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := ShareLocationSetting{Mode: ShareLocationFolder, Folder: "Clippings"}
	if saved.SharesLocation != want {
		t.Errorf("SetSettings returned %+v, want %+v", saved.SharesLocation, want)
	}
	got, err = v.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.SharesLocation != want {
		t.Errorf("GetSettings = %+v, want %+v", got.SharesLocation, want)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".zennotes", "vault.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"sharesLocation": {`) || !strings.Contains(string(raw), `"folder": "Clippings"`) {
		t.Errorf("vault.json is missing the shares location:\n%s", raw)
	}

	// A vault.json the desktop app wrote, with a value the TypeScript side
	// reads as Quick Notes, still loads instead of failing every settings read.
	if err := os.WriteFile(filepath.Join(root, ".zennotes", "vault.json"), []byte(`{"primaryNotesLocation":"inbox","sharesLocation":"Clippings","favorites":["inbox/A.md"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = v.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings on a malformed shares location: %v", err)
	}
	if got.SharesLocation != (ShareLocationSetting{Mode: ShareLocationQuick}) {
		t.Errorf("malformed shares location = %+v, want quick", got.SharesLocation)
	}
	if len(got.Favorites) != 1 {
		t.Errorf("the rest of vault.json was lost: favorites = %v", got.Favorites)
	}
}
