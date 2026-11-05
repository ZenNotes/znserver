package vault

import (
	"os"
	"path/filepath"
	"testing"
)

// A Markdown destination with a space or a parenthesis is not a link unless it is in
// angle brackets, and a rename can bring either in. A wikilink needs no brackets.
func TestRenameKeepsMarkdownLinksLinks(t *testing.T) {
	assets := []AssetMeta{{Path: "assets/old.png"}, {Path: "assets/old name.png"}}
	note := "Note.md"
	cases := []struct{ name, in, newPath, want string }{
		{"a space", "![](assets/old.png)", "assets/a b.png", "![](<assets/a b.png>)"},
		{"parentheses", "[x](assets/old.png)", "assets/a(1).png", "[x](<assets/a(1).png>)"},
		{"a title stays", `![](assets/old.png "Shot")`, "assets/a b.png", `![](<assets/a b.png> "Shot")`},
		{"a fragment stays", "[p](assets/old.png#page=2)", "assets/a b.png", "[p](<assets/a b.png#page=2>)"},
		{"a plain name needs none", "![](assets/old.png)", "assets/new.png", "![](assets/new.png)"},
		{"brackets stay", "![](<assets/old.png>)", "assets/new.png", "![](<assets/new.png>)"},
		{"a bracket in the name", "![](assets/old.png)", "assets/a >b.png", "![](<assets/a %3Eb.png>)"},
		{"wikilinks take none", "![[assets/old.png]]", "assets/a b.png", "![[assets/a b.png]]"},
		{"an encoded link stays encoded", "![](assets/old%20name.png)", "assets/a b.png", "![](assets/a%20b.png)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			old := "assets/old.png"
			if c.name == "an encoded link stays encoded" {
				old = "assets/old name.png"
			}
			got, n := rewriteAssetReferencesInBody(c.in, assets, note, old, c.newPath)
			if got != c.want || n != 1 {
				t.Errorf("got %q (%d), want %q", got, n, c.want)
			}
		})
	}
}

// A note holding only a bracketed link still counts as holding an attachment, or a
// rename skips it and the link goes stale. The server writes links like this itself.
func TestBracketedLinksCountAsAttachments(t *testing.T) {
	for body, want := range map[string]bool{
		"![](assets/a.png)":          true,
		"![](<assets/a.png>)":        true,
		"![](<assets/a b.png>)":      true,
		"[x](<assets/a b.png>)":      true,
		`![](<assets/a b.png> "T")`:  true,
		"![](<assets/a(1).png>)":     true,
		"[x](<https://e.com/a.png>)": false,
		"[x](<#heading>)":            false,
		"[x](<Other Note.md>)":       false,
	} {
		if got := BodyHasLocalAsset(body); got != want {
			t.Errorf("%s: %v, want %v", body, got, want)
		}
	}
}

// Through the real rename, a link written with brackets can be renamed again.
func TestRenameAssetToANameWithASpace(t *testing.T) {
	root := t.TempDir()
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "assets/pic.png", "png")
	writeTestFile(t, root, "Note.md", "![](assets/pic.png)\n")
	if _, err := v.RenameAsset("assets/pic.png", "a b.png"); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, root, "Note.md"); got != "![](<assets/a b.png>)\n" {
		t.Errorf("after the first rename: %q", got)
	}
	if _, err := v.RenameAsset("assets/a b.png", "c.png"); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, root, "Note.md"); got != "![](<assets/c.png>)\n" {
		t.Errorf("after the second rename: %q", got)
	}
}

func writeTestFile(t *testing.T, root, rel, body string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, root, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
