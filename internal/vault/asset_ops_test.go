package vault

import (
	"os"
	"path/filepath"
	"testing"
)

// writeAsset drops a file at a vault-relative path, creating parent dirs.
func writeAsset(t *testing.T, root, rel, body string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRenameAssetInPlace(t *testing.T) {
	root := t.TempDir()
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	writeAsset(t, root, "assets/pic.png", "PNG")

	meta, err := v.RenameAsset("assets/pic.png", "renamed.png")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Path != "assets/renamed.png" {
		t.Fatalf("renamed path = %q, want assets/renamed.png", meta.Path)
	}
	if meta.Kind != "image" {
		t.Errorf("kind = %q, want image", meta.Kind)
	}
	if _, err := os.Stat(filepath.Join(root, "assets", "renamed.png")); err != nil {
		t.Errorf("renamed file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "assets", "pic.png")); !os.IsNotExist(err) {
		t.Errorf("old file still present, err = %v", err)
	}
}

func TestRewriteAssetReferencesOnRename(t *testing.T) {
	assets := []AssetMeta{
		{Path: "assets/old.png"}, {Path: "assets/other.png"}, {Path: "assets/old name.png"},
		{Path: "assets/dup.png"}, {Path: "docs/dup.png"},
	}
	note := "inbox/Daily/2026-09-15.md"
	cases := []struct {
		in, want string
		changed  int
	}{
		{"Shot: ![[assets/old.png]]\n", "Shot: ![[assets/new.png]]\n", 1},
		{"![[assets/old.png|300]] [[assets/old.png|the shot]] [[/assets/old.png#top]]",
			"![[assets/new.png|300]] [[assets/new.png|the shot]] [[/assets/new.png#top]]", 3},
		{"![alt](assets/old.png \"Title\")\n[open](../../assets/old.png)\n[p](/assets/old.png#page=2)",
			"![alt](assets/new.png \"Title\")\n[open](../../assets/new.png)\n[p](/assets/new.png#page=2)", 3},
		{"[![shot](assets/old.png)](assets/old.png)", "[![shot](assets/new.png)](assets/new.png)", 2},
		{"![[old.png]] ![](old.png)", "![[new.png]] ![](new.png)", 2},
		{"![[dup.png]]", "![[dup.png]]", 0},
		{"`![[assets/old.png]]`\n```\n![[assets/old.png]]\n```\n~~~\n![](assets/old.png)\n~~~\n",
			"`![[assets/old.png]]`\n```\n![[assets/old.png]]\n```\n~~~\n![](assets/old.png)\n~~~\n", 0},
		{"![[assets/other.png]] [[Old Note]] [web](https://x/assets/old.png) ![](//cdn/assets/old.png)",
			"![[assets/other.png]] [[Old Note]] [web](https://x/assets/old.png) ![](//cdn/assets/old.png)", 0},
	}
	for _, c := range cases {
		got, n := rewriteAssetReferencesInBody(c.in, assets, note, "assets/old.png", "assets/new.png")
		if got != c.want || n != c.changed {
			t.Errorf("rewrite(%q) = %q (%d), want %q (%d)", c.in, got, n, c.want, c.changed)
		}
	}
	got, n := rewriteAssetReferencesInBody("![](assets/old%20name.png) ![](<assets/old name.png>) ![[assets/old name.png]]",
		assets, note, "assets/old name.png", "assets/new name.png")
	if want := "![](assets/new%20name.png) ![](<assets/new name.png>) ![[assets/new name.png]]"; got != want || n != 3 {
		t.Errorf("spaced rename = %q (%d), want %q (3)", got, n, want)
	}
	if got, n := rewriteAssetReferencesInBody("![[assets/old.png]]", assets, note, "assets/old.png", "assets/old.png"); got != "![[assets/old.png]]" || n != 0 {
		t.Errorf("same-name rename changed the body: %q (%d)", got, n)
	}
}

func TestRewriteAssetReferencesOnMove(t *testing.T) {
	assets := []AssetMeta{
		{Path: "assets/old.png"}, {Path: "assets/other.png"}, {Path: "assets/old name.png"},
		{Path: "assets/dup.png"}, {Path: "docs/dup.png"},
	}
	note := "inbox/Daily/2026-09-15.md"
	cases := []struct {
		in, want, newPath string
		changed           int
	}{
		// Vault-root wikilinks and hrefs re-root; a spelled-out leading slash stays.
		{"![[assets/old.png]] [[assets/old.png|the shot]] [p](/assets/old.png#page=2)",
			"![[media/shots/old.png]] [[media/shots/old.png|the shot]] [p](/media/shots/old.png#page=2)", "media/shots/old.png", 3},
		// Note-relative stays relative to the note.
		{"[open](../../assets/old.png \"Title\")", "[open](../../media/shots/old.png \"Title\")", "media/shots/old.png", 1},
		{"![](../../assets/old.png)", "![](old.png)", "inbox/Daily/old.png", 1},
		// A bare name stays bare while it still names one asset.
		{"![[old.png]] ![](old.png)", "![[old.png]] ![](old.png)", "media/shots/old.png", 0},
		// It spells out the path once the bare name would be ambiguous.
		{"![[old.png]] ![[assets/old.png]]", "![[docs/dup.png]] ![[docs/dup.png]]", "docs/dup.png", 2},
		{"![[old.png]]", "![[assets/dup.png]]", "assets/dup.png", 1},
	}
	for _, c := range cases {
		got, n := rewriteAssetReferencesInBody(c.in, assets, note, "assets/old.png", c.newPath)
		if got != c.want || n != c.changed {
			t.Errorf("move rewrite(%q → %q) = %q (%d), want %q (%d)", c.in, c.newPath, got, n, c.want, c.changed)
		}
	}
	// A note at the vault root writes the plain path either way.
	if got, _ := rewriteAssetReferencesInBody("![](assets/old.png)", assets, "Root.md", "assets/old.png", "media/old.png"); got != "![](media/old.png)" {
		t.Errorf("root-note move = %q", got)
	}
	// An asset that sat next to its note: hrefs go relative, wikilinks stay bare or go vault-root, never `..`.
	local := []AssetMeta{{Path: "inbox/pic.png"}, {Path: "inbox/sub/chart.png"}, {Path: "assets/other.png"}}
	if got, _ := rewriteAssetReferencesInBody("![[pic.png]] ![alt](pic.png) [[inbox/pic.png|the pic]] ![[assets/other.png]]", local, "inbox/Pics.md", "inbox/pic.png", "media/shots/pic.png"); got != "![[pic.png]] ![alt](../media/shots/pic.png) [[media/shots/pic.png|the pic]] ![[assets/other.png]]" {
		t.Errorf("same-folder move = %q", got)
	}
	if got, _ := rewriteAssetReferencesInBody("![[sub/chart.png]] ![](sub/chart.png)", local, "inbox/Pics.md", "inbox/sub/chart.png", "media/chart.png"); got != "![[media/chart.png]] ![](../media/chart.png)" {
		t.Errorf("relative-folder move = %q", got)
	}
	if got, _ := rewriteAssetReferencesInBody("![[pic.png]]", local, "inbox/Pics.md", "inbox/pic.png", "assets/other.png"); got != "![[assets/other.png]]" {
		t.Errorf("bare-collision move = %q", got)
	}
	got, n := rewriteAssetReferencesInBody("![](assets/old%20name.png) ![](<assets/old name.png>) `![[assets/old name.png]]`",
		assets, note, "assets/old name.png", "media/new name.png")
	if want := "![](media/new%20name.png) ![](<media/new name.png>) `![[assets/old name.png]]`"; got != want || n != 2 {
		t.Errorf("encoded move = %q (%d), want %q (2)", got, n, want)
	}
}

// End-to-end: a real MoveAsset re-targets the notes that referenced the asset
// in the author's style and leaves the rest alone. (#785)
func TestMoveAssetRewritesReferences(t *testing.T) {
	root := t.TempDir()
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	writeAsset(t, root, "assets/shot.png", "PNG")
	if _, err := v.WriteNote("inbox/Rooted.md", "![[assets/shot.png|300]]\n[page](/assets/shot.png#page=2)\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.WriteNote("inbox/Daily/2026-09-15.md", "![shot](../../assets/shot.png \"Shot\")\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.WriteNote("inbox/Bare.md", "See ![[shot.png]] and [the file](shot.png).\n"); err != nil {
		t.Fatal(err)
	}
	bareBefore, err := os.Stat(filepath.Join(root, "inbox", "Bare.md"))
	if err != nil {
		t.Fatal(err)
	}

	meta, err := v.MoveAsset("assets/shot.png", "media/screenshots")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Path != "media/screenshots/shot.png" {
		t.Fatalf("moved path = %q, want media/screenshots/shot.png", meta.Path)
	}
	rooted, err := v.ReadNote("inbox/Rooted.md")
	if err != nil {
		t.Fatal(err)
	}
	if want := "![[media/screenshots/shot.png|300]]\n[page](/media/screenshots/shot.png#page=2)\n"; rooted.Body != want {
		t.Fatalf("Rooted after move =\n%q\nwant\n%q", rooted.Body, want)
	}
	daily, err := v.ReadNote("inbox/Daily/2026-09-15.md")
	if err != nil {
		t.Fatal(err)
	}
	if want := "![shot](../../media/screenshots/shot.png \"Shot\")\n"; daily.Body != want {
		t.Fatalf("Daily after move = %q, want %q", daily.Body, want)
	}
	bareAfter, err := os.Stat(filepath.Join(root, "inbox", "Bare.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bareAfter.ModTime().Equal(bareBefore.ModTime()) {
		t.Errorf("bare-name note was rewritten though its links still resolve")
	}
}

// End-to-end: a real RenameAsset rewrites the notes that referenced the asset
// and leaves the rest alone. (#785)
func TestRenameAssetRewritesReferences(t *testing.T) {
	root := t.TempDir()
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	writeAsset(t, root, "assets/shot.png", "PNG")
	writeAsset(t, root, "assets/other.png", "PNG")
	body := "![[assets/shot.png]] ![[assets/shot.png|300]] ![alt](assets/shot.png \"Shot\") [[assets/shot.png|open]] ![[shot.png]]\n\n`![[assets/shot.png]]` ![[assets/other.png]]\n"
	if _, err := v.WriteNote("inbox/Embeds.md", body); err != nil {
		t.Fatal(err)
	}
	// A plain file link is neither an embed nor a note wikilink: HasAttachments carries it.
	if _, err := v.WriteNote("inbox/Bare.md", "See [the file](shot.png).\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.WriteNote("inbox/Plain.md", "No pictures, just [[Embeds]].\n"); err != nil {
		t.Fatal(err)
	}
	plainBefore, err := os.Stat(filepath.Join(root, "inbox", "Plain.md"))
	if err != nil {
		t.Fatal(err)
	}

	meta, err := v.RenameAsset("assets/shot.png", "screenshot.png")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Path != "assets/screenshot.png" {
		t.Fatalf("renamed path = %q, want assets/screenshot.png", meta.Path)
	}

	got, err := v.ReadNote("inbox/Embeds.md")
	if err != nil {
		t.Fatal(err)
	}
	want := "![[assets/screenshot.png]] ![[assets/screenshot.png|300]] ![alt](assets/screenshot.png \"Shot\") [[assets/screenshot.png|open]] ![[screenshot.png]]\n\n`![[assets/shot.png]]` ![[assets/other.png]]\n"
	if got.Body != want {
		t.Fatalf("Embeds after rename =\n%q\nwant\n%q", got.Body, want)
	}
	bare, err := v.ReadNote("inbox/Bare.md")
	if err != nil {
		t.Fatal(err)
	}
	if bare.Body != "See [the file](screenshot.png).\n" {
		t.Fatalf("Bare after rename = %q", bare.Body)
	}
	plainAfter, err := os.Stat(filepath.Join(root, "inbox", "Plain.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !plainAfter.ModTime().Equal(plainBefore.ModTime()) {
		t.Errorf("a note without references was rewritten")
	}
}

func TestRenameAssetRejectsCollision(t *testing.T) {
	root := t.TempDir()
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	writeAsset(t, root, "assets/a.png", "A")
	writeAsset(t, root, "assets/b.png", "B")

	if _, err := v.RenameAsset("assets/a.png", "b.png"); err == nil {
		t.Fatal("expected collision error, got nil")
	}
	// Both originals must still be intact.
	if _, err := os.Stat(filepath.Join(root, "assets", "a.png")); err != nil {
		t.Errorf("source lost after failed rename: %v", err)
	}
}

func TestRenameAssetRejectsMarkdownAndDotDot(t *testing.T) {
	root := t.TempDir()
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	writeAsset(t, root, "assets/pic.png", "PNG")

	if _, err := v.RenameAsset("assets/pic.png", "note.md"); err == nil {
		t.Error("expected error renaming asset to a .md name")
	}
	if _, err := v.RenameAsset("assets/pic.png", "sub/dir.png"); err == nil {
		t.Error("expected error for a name containing a path separator")
	}
	if _, err := v.RenameAsset("inbox/Note.md", "x.png"); err == nil {
		t.Error("expected error renaming a markdown note through RenameAsset")
	}
}

func TestMoveAssetIntoFolder(t *testing.T) {
	root := t.TempDir()
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	writeAsset(t, root, "assets/pic.png", "PNG")

	meta, err := v.MoveAsset("assets/pic.png", "media/screens")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Path != "media/screens/pic.png" {
		t.Fatalf("moved path = %q, want media/screens/pic.png", meta.Path)
	}
	if _, err := os.Stat(filepath.Join(root, "media", "screens", "pic.png")); err != nil {
		t.Errorf("moved file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "assets", "pic.png")); !os.IsNotExist(err) {
		t.Errorf("source still present after move, err = %v", err)
	}
}

func TestMoveAssetEmptyTargetGoesToAssetsDir(t *testing.T) {
	root := t.TempDir()
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// A loose asset at the vault root, as a Vault Root-mode drop can leave.
	writeAsset(t, root, "pic.png", "PNG")

	meta, err := v.MoveAsset("pic.png", "")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Path != "assets/pic.png" {
		t.Fatalf("moved path = %q, want assets/pic.png", meta.Path)
	}
}

func TestMoveAssetUniquifiesOnCollision(t *testing.T) {
	root := t.TempDir()
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	writeAsset(t, root, "assets/pic.png", "SRC")
	writeAsset(t, root, "media/pic.png", "EXISTING")

	meta, err := v.MoveAsset("assets/pic.png", "media")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Path != "media/pic 2.png" {
		t.Fatalf("moved path = %q, want media/pic 2.png", meta.Path)
	}
	// The pre-existing file must be untouched.
	body, err := os.ReadFile(filepath.Join(root, "media", "pic.png"))
	if err != nil || string(body) != "EXISTING" {
		t.Errorf("pre-existing file clobbered: body=%q err=%v", body, err)
	}
}

func TestMoveAssetSameDirIsNoop(t *testing.T) {
	root := t.TempDir()
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	writeAsset(t, root, "assets/pic.png", "PNG")

	meta, err := v.MoveAsset("assets/pic.png", "assets")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Path != "assets/pic.png" {
		t.Fatalf("no-op move path = %q, want assets/pic.png", meta.Path)
	}
}

func TestFolderColorsRoundTripAndValidation(t *testing.T) {
	root := t.TempDir()
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.SetSettings(VaultSettings{
		FolderColors: map[string]FolderColorID{
			"inbox:Projects": "violet",
			"inbox:Bad":      "chartreuse", // not a preset — must be dropped
			"":               "blue",       // empty key — must be dropped
		},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := v.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.FolderColors["inbox:Projects"] != "violet" {
		t.Fatalf("folderColors did not round-trip: %v", got.FolderColors)
	}
	if _, ok := got.FolderColors["inbox:Bad"]; ok {
		t.Error("invalid color id was persisted")
	}
	if _, ok := got.FolderColors[""]; ok {
		t.Error("empty-key color was persisted")
	}
}

func TestFolderColorsFollowFolderRename(t *testing.T) {
	root := t.TempDir()
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "inbox", "Projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := v.SetSettings(VaultSettings{
		FolderColors: map[string]FolderColorID{"inbox:Projects": "teal"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := v.RenameFolder("inbox", "Projects", "Work"); err != nil {
		t.Fatal(err)
	}
	got, err := v.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.FolderColors["inbox:Work"] != "teal" {
		t.Errorf("color did not follow rename: %v", got.FolderColors)
	}
	if _, ok := got.FolderColors["inbox:Projects"]; ok {
		t.Error("stale color key survived rename")
	}
}

func TestFolderColorsPrunedOnDeleteAndCopiedOnDuplicate(t *testing.T) {
	root := t.TempDir()
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "inbox", "Projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := v.SetSettings(VaultSettings{
		FolderColors: map[string]FolderColorID{"inbox:Projects": "pink"},
	}); err != nil {
		t.Fatal(err)
	}

	rel, err := v.DuplicateFolder("inbox", "Projects")
	if err != nil {
		t.Fatal(err)
	}
	dup, err := v.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if dup.FolderColors["inbox:"+rel] != "pink" {
		t.Errorf("duplicate did not inherit color (key inbox:%s): %v", rel, dup.FolderColors)
	}
	if dup.FolderColors["inbox:Projects"] != "pink" {
		t.Errorf("source color lost after duplicate: %v", dup.FolderColors)
	}

	if err := v.DeleteFolder("inbox", "Projects"); err != nil {
		t.Fatal(err)
	}
	del, err := v.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := del.FolderColors["inbox:Projects"]; ok {
		t.Error("deleted folder's color was not pruned")
	}
}
