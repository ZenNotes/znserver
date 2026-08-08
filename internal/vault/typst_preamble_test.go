package vault

import "testing"

// These cases mirror packages/shared-domain/src/typst-preamble-folder.test.ts
// one for one. When either side gains a rule, add it here too: the two
// implementations only stay compatible if they are tested on the same inputs.

func TestNormalizeTypstPreambleFolder(t *testing.T) {
	valid := map[string]string{
		"typst":         "typst",
		"  Preambles  ": "Preambles",
		"math defs":     "math defs",
	}
	for input, want := range valid {
		if got := normalizeTypstPreambleFolder(input); got != want {
			t.Errorf("normalizeTypstPreambleFolder(%q) = %q, want %q", input, got, want)
		}
	}

	invalid := []string{
		"a/b", `a\b`, "/typst", ".", "..", ".hidden", "", "   ",
		"a:b", "a*b", "a#b", "a[b]",
	}
	for _, input := range invalid {
		if got := normalizeTypstPreambleFolder(input); got != "" {
			t.Errorf("normalizeTypstPreambleFolder(%q) = %q, want \"\"", input, got)
		}
	}

	long := make([]byte, 129)
	for i := range long {
		long[i] = 'x'
	}
	if got := normalizeTypstPreambleFolder(string(long)); got != "" {
		t.Errorf("129-char name accepted: %q", got)
	}
	if got := normalizeTypstPreambleFolder(string(long[:128])); got != string(long[:128]) {
		t.Errorf("128-char name rejected")
	}
}

func TestResolveTypstPreambleFolder(t *testing.T) {
	if got := resolveTypstPreambleFolder(VaultSettings{}); got != DefaultTypstPreambleFolder {
		t.Errorf("absent settings = %q, want %q", got, DefaultTypstPreambleFolder)
	}
	bad := VaultSettings{TypstPreambles: &TypstPreambleSettings{Folder: "a/b"}}
	if got := resolveTypstPreambleFolder(bad); got != DefaultTypstPreambleFolder {
		t.Errorf("invalid folder = %q, want the default", got)
	}
	ok := VaultSettings{TypstPreambles: &TypstPreambleSettings{Folder: "Preambles"}}
	if got := resolveTypstPreambleFolder(ok); got != "Preambles" {
		t.Errorf("override = %q, want Preambles", got)
	}
}

func TestNormalizeTypstPreambleSettings(t *testing.T) {
	// The default never persists, so an untouched vault.json grows no stub.
	for _, in := range []*TypstPreambleSettings{
		nil,
		{Folder: "typst"},
		{Folder: ""},
		{Folder: "a/b"},
	} {
		if got := normalizeTypstPreambleSettings(in); got != nil {
			t.Errorf("normalizeTypstPreambleSettings(%v) = %+v, want nil", in, got)
		}
	}
	got := normalizeTypstPreambleSettings(&TypstPreambleSettings{Folder: " Preambles "})
	if got == nil || got.Folder != "Preambles" {
		t.Errorf("override lost: %+v", got)
	}
}

func TestIsTypstPreamblePath(t *testing.T) {
	cases := []struct {
		path   string
		folder string
		want   bool
	}{
		{"typst/physics.md", "typst", true},
		{"inbox/typst/physics.md", "typst", true},
		{"archive/notes/TYPST/maths.md", "typst", true},
		// The file itself is never the folder.
		{"inbox/typst.md", "typst", false},
		{"typst", "typst", false},
		// Exact segment match, not a prefix.
		{"inbox/typstish/x.md", "typst", false},
		{"inbox/my-typst/x.md", "typst", false},
		// A renamed folder moves the exclusion with it.
		{"inbox/Preambles/physics.md", "Preambles", true},
		{"inbox/typst/physics.md", "Preambles", false},
		// An empty name is inert rather than matching everything.
		{"inbox/typst/physics.md", "", false},
	}
	for _, c := range cases {
		if got := isTypstPreamblePath(c.path, c.folder); got != c.want {
			t.Errorf("isTypstPreamblePath(%q, %q) = %v, want %v", c.path, c.folder, got, c.want)
		}
	}
}
