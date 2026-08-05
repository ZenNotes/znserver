package vault

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// #458: mirrors tasks-excluded-folders.test.ts in shared-domain; the rules
// must stay byte-compatible across runtimes.

func TestNormalizeTasksExcludedFolders(t *testing.T) {
	got := normalizeTasksExcludedFolders([]string{
		"inbox/Books",
		"../x",
		"inbox/Books/",
		"/inbox//Books",
		"inbox\\Books",
		"archive/Old",
		"./inbox",
		"   ",
	})
	want := []string{"inbox/Books", "archive/Old"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("normalizeTasksExcludedFolders = %v, want %v", got, want)
	}
}

func TestIsPathExcludedFromTasks(t *testing.T) {
	excluded := []string{"inbox/Books", "archive/Old Projects"}
	cases := []struct {
		path string
		want bool
	}{
		{"inbox/Books/dune.md", true},
		{"inbox/Books/scifi/blindsight.md", true},
		{"archive/Old Projects/site.md", true},
		{"inbox/Bookshelf.md", false},
		{"inbox/Books.md", false},
		{"inbox/books/dune.md", false}, // case-sensitive
		{"quick/note.md", false},
	}
	for _, tc := range cases {
		if got := isPathExcludedFromTasks(tc.path, excluded); got != tc.want {
			t.Errorf("isPathExcludedFromTasks(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
	if isPathExcludedFromTasks("inbox/Books/dune.md", nil) {
		t.Error("empty exclusion list must never match")
	}
}

// End to end through New → GetSettings → ScanTasks, so the settings cache and
// cloneSettings are on the hook too: the defensive copy silently dropped the
// Tasks object once, and only a live-server smoke test caught it.
func TestScanTasksHonorsExcludedFolders(t *testing.T) {
	root := t.TempDir()
	mustWrite := func(rel, body string) {
		t.Helper()
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(".zennotes/vault.json", `{"tasks":{"excludedFolders":["inbox/Books"]}}`)
	mustWrite("inbox/Real Work.md", "- [ ] ship it\n")
	mustWrite("inbox/Books/Backlog.md", "- [ ] Excession\n- [ ] Player of Games\n")

	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}

	tasks, err := v.ScanTasks()
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range tasks {
		if isPathExcludedFromTasks(tk.SourcePath, []string{"inbox/Books"}) {
			t.Errorf("excluded-folder task leaked into the default scan: %s", tk.ID)
		}
	}
	if len(tasks) != 1 {
		t.Fatalf("expected only the Real Work task, got %d tasks", len(tasks))
	}

	all, err := v.ScanTasksWith(ParseTasksOptions{IncludeExcluded: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 tasks with IncludeExcluded, got %d", len(all))
	}
}
