package vault

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The tour named its notes "NN <long dash> Title" until 2.68.0. Built from an
// escape so this file never holds the character itself.
const longDash = "\u2014"

func demoNames(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "inbox", "demo"))
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func vaultWithOlderTour(t *testing.T) (*Vault, string) {
	t.Helper()
	root := t.TempDir()
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"inbox/demo/00 " + longDash + " Start Here.md",
		"inbox/demo/05b " + longDash + " Math Diagrams.md",
		"inbox/demo/My own note.md",
		// Someone's own note: only the old separator marks a tour note.
		"inbox/demo/00 \u00b7 Start Here.md",
	} {
		writeAsset(t, root, rel, "older\n")
	}
	return v, root
}

func TestDemoTourNamesCarryNoLongDash(t *testing.T) {
	tour, err := loadDemoTour()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range append(tour.Notes, tour.Assets...) {
		if strings.Contains(f.Path, longDash) || strings.Contains(f.Body, longDash) {
			t.Errorf("%s still holds the long dash", f.Path)
		}
	}
}

func TestGenerateDemoTourReplacesOlderNames(t *testing.T) {
	v, root := vaultWithOlderTour(t)
	if _, err := v.GenerateDemoTour(); err != nil {
		t.Fatal(err)
	}
	names := demoNames(t, root)
	for _, name := range names {
		if strings.Contains(name, longDash) {
			t.Errorf("older tour note left beside the new one: %q", name)
		}
	}
	for _, want := range []string{"00 Start Here.md", "05b Math Diagrams.md", "My own note.md", "00 \u00b7 Start Here.md"} {
		if !contains(names, want) {
			t.Errorf("missing %q in %v", want, names)
		}
	}
}

func TestRemoveDemoTourRemovesOlderNames(t *testing.T) {
	v, root := vaultWithOlderTour(t)
	result, err := v.RemoveDemoTour()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"inbox/demo/00 " + longDash + " Start Here.md", "inbox/demo/05b " + longDash + " Math Diagrams.md"} {
		if !contains(result.NotePaths, want) {
			t.Errorf("result misses %q: %v", want, result.NotePaths)
		}
	}
	names := demoNames(t, root)
	if want := []string{"00 \u00b7 Start Here.md", "My own note.md"}; strings.Join(names, "|") != strings.Join(want, "|") {
		t.Errorf("left %v, want %v", names, want)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestRemoveDemoTourLeavesNoTourFolder(t *testing.T) {
	root := t.TempDir()
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.GenerateDemoTour(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "inbox", "demo", "Reading List.base", "data.csv")); err != nil {
		t.Fatalf("the sample database was not written: %v", err)
	}
	if _, err := v.RemoveDemoTour(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "inbox", "demo")); !os.IsNotExist(err) {
		t.Errorf("the tour folder is still there (err %v)", err)
	}
}
