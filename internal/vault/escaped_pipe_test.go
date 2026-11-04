package vault

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A table cell escapes a wikilink's separator as `\|`, because GFM would end the
// cell at a bare pipe. The app never creates a name with a backslash, so one right
// before the pipe is read as the escape and never as part of the target.

func TestExtractWikilinksReadsAnEscapedPipe(t *testing.T) {
	cases := []struct {
		name, body string
		want       []string
	}{
		{"table cell", `| [[C\|d]] | x |`, []string{"C"}},
		{"bare, outside a table", `see [[C\|d]] here`, []string{"C"}},
		{"anchor", `| [[C#Heading\|d]] |`, []string{"C#Heading"}},
		{"block anchor", `| [[C^block\|d]] |`, []string{"C^block"}},
		{"path", `| [[notes/C.md\|d]] |`, []string{"notes/C.md"}},
		{"alias with more pipes", `[[C\|d\|e]]`, []string{"C"}},
		{"both forms are one link", `[[C|d]] and | [[C\|e]] |`, []string{"C"}},
		{"unescaped alias", `[[C|d]]`, []string{"C"}},
		{"unescaped alias holding an escape", `[[C|d\|e]]`, []string{"C"}},
		{"plain", `[[C]]`, []string{"C"}},
		{"a backslash with no pipe stays", `[[C\]]`, []string{`C\`}},
		{"a backslash inside a target stays", `[[a\b|d]]`, []string{`a\b`}},
		{"inline code", "`[[C\\|d]]`", []string{}},
		{"a separator with no target", `[[\|d]]`, []string{}},
		{"a space with no target", `[[ \|d]]`, []string{}},
		{"a backslash alone", `[[\]]`, []string{}},
		// An embedded asset is reported under AssetEmbeds, not as a link.
		{"asset embed", `| ![[pic.png\|300]] |`, []string{}},
		{"note embed", `| ![[C\|300]] |`, []string{"C"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractWikilinks(tc.body); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ExtractWikilinks(%q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

func TestAssetExtractorsReadAnEscapedPipe(t *testing.T) {
	body := `| ![[pic.png\|300]] | ![[design.psd\|Source]] | ![[Note\|300]] |`
	if got, want := ExtractAssetEmbeds(body), []string{"pic.png", "design.psd"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ExtractAssetEmbeds = %q, want %q", got, want)
	}
	for in, want := range map[string]bool{
		`| ![[pic.png\|300]] |`: true,
		`![[pic.png|300]]`:      true,
		`| ![[Note\|300]] |`:    false,
		"`![[pic.png\\|300]]`":  false,
	} {
		if got := BodyHasLocalAsset(in); got != want {
			t.Errorf("BodyHasLocalAsset(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestRewriteAssetReferencesKeepsAnEscapedPipe(t *testing.T) {
	assets := []AssetMeta{{Path: "assets/pic.png"}, {Path: "assets/other.png"}}
	cases := []struct {
		name, in, newPath, want string
		changed                 int
	}{
		{"rename", `| ![[pic.png\|300]] |`, "assets/new.png", `| ![[new.png\|300]] |`, 1},
		{"rooted path", `| ![[assets/pic.png\|300]] |`, "assets/new.png", `| ![[assets/new.png\|300]] |`, 1},
		{"move", `| ![[assets/pic.png\|300]] |`, "media/pic.png", `| ![[media/pic.png\|300]] |`, 1},
		{"a bare name that still resolves", `| ![[pic.png\|300]] |`, "media/pic.png", `| ![[pic.png\|300]] |`, 0},
		{"fragment", `| ![[pic.png#page=2\|300]] |`, "assets/new.png", `| ![[new.png#page=2\|300]] |`, 1},
		{"plain link", `| [[pic.png\|the picture]] |`, "assets/new.png", `| [[new.png\|the picture]] |`, 1},
		{"unescaped stays unescaped", `![[pic.png|300]]`, "assets/new.png", `![[new.png|300]]`, 1},
		{"code stays", "`![[pic.png\\|300]]`", "assets/new.png", "`![[pic.png\\|300]]`", 0},
		{"another asset stays", `| ![[other.png\|300]] |`, "assets/new.png", `| ![[other.png\|300]] |`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, n := rewriteAssetReferencesInBody(tc.in, assets, "notes/n.md", "assets/pic.png", tc.newPath)
			if got != tc.want || n != tc.changed {
				t.Fatalf("rewrote %d, got %s, want %d, %s", n, got, tc.changed, tc.want)
			}
		})
	}
}

func TestRenameAssetRewritesAnEscapedPipeInATable(t *testing.T) {
	v, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(v.Root(), "assets", "pic.png")
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	const before = "| Shot | Note |\n| --- | --- |\n| ![[pic.png\\|300]] | x |\n"
	meta, err := v.WriteNote("inbox/Table.md", before)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.HasAttachments || !reflect.DeepEqual(meta.AssetEmbeds, []string{"pic.png"}) {
		t.Fatalf("the embed was not indexed: %+v", meta)
	}
	if _, err := v.RenameAsset("assets/pic.png", "new.png"); err != nil {
		t.Fatal(err)
	}
	got, err := v.ReadNote("inbox/Table.md")
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(before, "pic.png", "new.png", 1); got.Body != want {
		t.Fatalf("body = %q, want %q", got.Body, want)
	}
}

func TestRenameNoteRewritesAnEscapedPipeInATable(t *testing.T) {
	notes := renameTestNotes()
	cases := []struct {
		name, in, want string
		changed        int
	}{
		{"table", `| [[Old Title\|d]] | x |`, `| [[New Title\|d]] | x |`, 1},
		{"anchor", `| [[Old Title#Intro\|d]] |`, `| [[New Title#Intro\|d]] |`, 1},
		{"path", `| [[demo/Old Title.md\|d]] |`, `| [[demo/New Title.md\|d]] |`, 1},
		{"embed", `| ![[Old Title\|d]] |`, `| ![[New Title\|d]] |`, 1},
		{"unescaped stays unescaped", `[[Old Title|d]]`, `[[New Title|d]]`, 1},
		{"code stays", "`[[Old Title\\|d]]`", "`[[Old Title\\|d]]`", 0},
		{"backslash in the path", `| [[demo\Old Title.md\|d]] |`, `| [[demo\New Title.md\|d]] |`, 1},
		{"backslash in the path, unescaped", `[[demo\Old Title.md|d]]`, `[[demo\New Title.md|d]]`, 1},
		{"another note stays", `| [[Other\|d]] |`, `| [[Other\|d]] |`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, n := rewriteWikilinksForRename(tc.in, notes, "inbox/demo/Old Title.md", "New Title")
			if got != tc.want || n != tc.changed {
				t.Fatalf("rewrote %d, got %s, want %d, %s", n, got, tc.changed, tc.want)
			}
		})
	}
}

// A vault indexed before the fix holds `C\` in its persisted cache, under the
// file's old mtime and size, so only a new cache version reads the note again.
func TestListNotesRebuildsACacheHoldingAnEscapedPipeTarget(t *testing.T) {
	v, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	meta, err := v.WriteNote("inbox/Table.md", "| [[C\\|d]] | x |\n")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(v.Root(), filepath.FromSlash(meta.Path)))
	if err != nil {
		t.Fatal(err)
	}
	meta.Wikilinks = []string{`C\`}
	raw, err := json.Marshal(persistedNoteMetaCache{
		Version: 2,
		Entries: []persistedNoteMetaEntry{{Path: meta.Path, MtimeMs: mtimeMs(info), Size: info.Size(), Meta: meta}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cachePath := v.noteMetaCachePath()
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	v.invalidateNoteMetaCache()
	notes, err := v.ListNotes()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := findNoteMeta(notes, meta.Path)
	if !ok || !reflect.DeepEqual(got.Wikilinks, []string{"C"}) {
		t.Fatalf("wikilinks = %q, want C", got.Wikilinks)
	}
}
