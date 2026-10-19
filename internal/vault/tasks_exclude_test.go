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
		if got := isPathExcludedFromTasks(tc.path, excluded, nil); got != tc.want {
			t.Errorf("isPathExcludedFromTasks(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
	if isPathExcludedFromTasks("inbox/Books/dune.md", nil, nil) {
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
		if isPathExcludedFromTasks(tk.SourcePath, []string{"inbox/Books"}, nil) {
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

// #962: the Task folders, mirroring the shared-domain cases.
func TestIsPathExcludedFromTasksWithTaskFolders(t *testing.T) {
	cases := []struct {
		path     string
		excluded []string
		included []string
		want     bool
	}{
		{"inbox/Idea.md", nil, nil, false},
		{"Tasks/Today.md", nil, []string{"Tasks"}, false},
		{"Tasks/Work/Q4.md", nil, []string{"Tasks"}, false},
		{"inbox/Books.md", nil, []string{"Tasks"}, true},
		{"TasksArchive/Old.md", nil, []string{"Tasks"}, true},
		{"inbox/Projects/Site.md", nil, []string{"Tasks", "inbox/Projects"}, false},
		{"Projects/Archive/Old.md", []string{"Projects/Archive"}, []string{"Projects"}, true},
		{"Projects/Live.md", []string{"Projects/Archive"}, []string{"Projects"}, false},
	}
	for _, tc := range cases {
		if got := isPathExcludedFromTasks(tc.path, tc.excluded, tc.included); got != tc.want {
			t.Errorf("isPathExcludedFromTasks(%q, %v, %v) = %v, want %v", tc.path, tc.excluded, tc.included, got, tc.want)
		}
	}
}

// End to end through New, GetSettings (cache plus cloneSettings, the copy
// that once dropped Tasks) and both scans.
func TestScanTasksHonorsTaskFolders(t *testing.T) {
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
	mustWrite(".zennotes/vault.json", `{"tasks":{"includedFolders":["inbox/Tasks"],"excludedFolders":["inbox/Tasks/Someday"]}}`)
	mustWrite("inbox/Tasks/Today.md", "- [ ] ship it\n")
	mustWrite("inbox/Tasks/Someday/Maybe.md", "- [ ] later\n")
	mustWrite("inbox/Reading.md", "- [ ] Excession\n- [ ] Player of Games\n")

	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	settings, err := v.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got := tasksIncludedFolders(settings); !reflect.DeepEqual(got, []string{"inbox/Tasks"}) {
		t.Fatalf("GetSettings dropped the Task folders: %v", got)
	}

	tasks, err := v.ScanTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].SourcePath != "inbox/Tasks/Today.md" {
		t.Fatalf("expected only the Today task, got %+v", tasks)
	}

	for rel, want := range map[string]int{
		"inbox/Tasks/Today.md":         1,
		"inbox/Tasks/Someday/Maybe.md": 0,
		"inbox/Reading.md":             0,
	} {
		got, err := v.ScanTasksForPath(rel)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != want {
			t.Errorf("ScanTasksForPath(%q) = %d tasks, want %d", rel, len(got), want)
		}
	}

	all, err := v.ScanTasksWith(ParseTasksOptions{IncludeExcluded: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("expected 4 tasks with IncludeExcluded, got %d", len(all))
	}
}

// #962: renaming a Task folder through the vault API carries both lists along,
// end to end through SetSettings and GetSettings.
func TestRenameFolderRewritesTaskFolders(t *testing.T) {
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
	mustWrite(".zennotes/vault.json", `{"tasks":{"includedFolders":["inbox/Tasks","inbox/TasksArchive"],"excludedFolders":["inbox/Tasks/Someday"]}}`)
	mustWrite("inbox/Tasks/Today.md", "- [ ] ship it\n")
	mustWrite("inbox/Tasks/Someday/Maybe.md", "- [ ] later\n")
	mustWrite("inbox/TasksArchive/Old.md", "- [x] done\n")

	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.RenameFolder(FolderInbox, "Tasks", "Todo"); err != nil {
		t.Fatal(err)
	}
	settings, err := v.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := tasksIncludedFolders(settings), []string{"inbox/Todo", "inbox/TasksArchive"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Task folders after rename = %v, want %v", got, want)
	}
	if got, want := tasksExcludedFolders(settings), []string{"inbox/Todo/Someday"}; !reflect.DeepEqual(got, want) {
		t.Errorf("excluded folders after rename = %v, want %v", got, want)
	}
	tasks, err := v.ScanTasks()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, tk := range tasks {
		found[tk.SourcePath] = true
	}
	if !found["inbox/Todo/Today.md"] || found["inbox/Todo/Someday/Maybe.md"] {
		t.Errorf("tasks after rename = %v", found)
	}
}
