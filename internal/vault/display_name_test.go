package vault

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These cases mirror packages/shared-domain/src/vault-display-name.test.ts one
// for one. When either side gains a rule, add it here too: the two
// implementations only stay compatible if they are tested on the same inputs.

func TestNormalizeVaultDisplayName(t *testing.T) {
	cases := map[string]string{
		"Acme API docs":                   "Acme API docs",
		"Été 2026 · notes":                "Été 2026 · notes",
		"  Acme   API\tdocs \n":           "Acme API docs",
		"Acme\x00 docs\u2028v2\x07":       "Acme docs v2",
		"A \x07 B":                        "A B",
		"":                                "",
		"   ":                             "",
		"\x07":                            "",
		"\ufeff":                          "",
		strings.Repeat("x", 64):           strings.Repeat("x", 64),
		strings.Repeat("x", 63) + "😀tail": strings.Repeat("x", 63),
		strings.Repeat("x", 62) + "😀tail": strings.Repeat("x", 62) + "😀",
	}
	for input, want := range cases {
		if got := normalizeVaultDisplayName(input); got != want {
			t.Errorf("normalizeVaultDisplayName(%q) = %q, want %q", input, got, want)
		}
	}

	long := strings.Repeat("word ", 20) + "end"
	got := normalizeVaultDisplayName(long)
	if units := utf16Len(got); units > maxVaultDisplayNameLength {
		t.Errorf("long name kept %d UTF-16 units, want at most %d", units, maxVaultDisplayNameLength)
	}
	if got != strings.TrimRight(got, " ") {
		t.Errorf("cut left a trailing space: %q", got)
	}
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xffff {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func TestResolveVaultName(t *testing.T) {
	root := filepath.Join("repos", "acme", "docs")
	if got := resolveVaultName(VaultSettings{DisplayName: "Acme API docs"}, root); got != "Acme API docs" {
		t.Errorf("display name = %q", got)
	}
	if got := resolveVaultName(VaultSettings{DisplayName: "  "}, root); got != "docs" {
		t.Errorf("blank display name = %q, want the folder", got)
	}
	if got := resolveVaultName(VaultSettings{}, root); got != "docs" {
		t.Errorf("no display name = %q, want the folder", got)
	}
}

// A desktop-written display name must survive a web-side settings save, and
// the vault must answer with it: the #446/#379 round-trip rule for #692.
func TestVaultDisplayNameRoundTripAndInfo(t *testing.T) {
	root := t.TempDir()
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := v.Info().Name; got != filepath.Base(root) {
		t.Fatalf("Info before a name = %q, want the folder %q", got, filepath.Base(root))
	}

	returned, err := v.SetSettings(VaultSettings{PrimaryNotesLocation: PrimaryNotesInbox, DisplayName: "  Acme   API docs "})
	if err != nil {
		t.Fatal(err)
	}
	if returned.DisplayName != "Acme API docs" {
		t.Errorf("SetSettings returned %q", returned.DisplayName)
	}
	saved, err := v.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if saved.DisplayName != "Acme API docs" {
		t.Errorf("GetSettings = %q", saved.DisplayName)
	}
	if got := v.Info().Name; got != "Acme API docs" {
		t.Errorf("Info = %q, want the display name", got)
	}

	// The web client's usual save: everything it read, written back.
	if _, err := v.SetSettings(saved); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".zennotes", "vault.json"))
	if err != nil {
		t.Fatal(err)
	}
	var onDisk map[string]any
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk["displayName"] != "Acme API docs" {
		t.Errorf("vault.json displayName after a round-trip = %v", onDisk["displayName"])
	}

	// Cleared: the key goes away rather than staying empty, and Info falls
	// back to the folder.
	saved.DisplayName = "   "
	if _, err := v.SetSettings(saved); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(root, ".zennotes", "vault.json"))
	if strings.Contains(string(raw), "displayName") {
		t.Errorf("cleared name still in vault.json: %s", raw)
	}
	if got := v.Info().Name; got != filepath.Base(root) {
		t.Errorf("Info after clearing = %q, want the folder", got)
	}
}
