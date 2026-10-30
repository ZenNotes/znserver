package vault

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The cases of shared-domain's template-folder.test.ts, with the answers the
// TypeScript normalizeTemplateFolder gives (null is "").
func TestNormalizeTemplateFolder(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Templates", "Templates"},
		{"inbox/Templates", "inbox/Templates"},
		{"  Meta / My Templates ", "Meta/My Templates"},
		{"/Templates/", "Templates"},
		{`Meta\Templates`, "Meta/Templates"},
		{"Meta//Templates", "Meta/Templates"},
		{"", ""},
		{"   ", ""},
		{"/", ""},
		{".", ""},
		{"..", ""},
		{"../Templates", ""},
		{"Templates/..", ""},
		{".zennotes/templates", ""},
		{"Meta/.hidden", ""},
		// JavaScript's trim(), not Go's TrimSpace: NBSP, BOM and the Unicode
		// separators go; NEL (U+0085) is not JavaScript whitespace and stays.
		{"\u00a0Templates\ufeff", "Templates"},
		{"\u0085Templates", "\u0085Templates"},
		{strings.Repeat("a", 512), strings.Repeat("a", 512)},
		{strings.Repeat("a", 513), ""},
		// Counted in UTF-16 code units: 256 astral characters are 512 units.
		{strings.Repeat("😀", 256), strings.Repeat("😀", 256)},
		{strings.Repeat("😀", 257), ""},
	}
	for _, tc := range cases {
		if got := normalizeTemplateFolder(tc.in); got != tc.want {
			t.Errorf("normalizeTemplateFolder(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsPathInTemplateFolder(t *testing.T) {
	cases := []struct {
		path, folder string
		want         bool
	}{
		{"Templates/Meeting.md", "Templates", true},
		{"Templates/Work/Standup.md", "Templates", true},
		{"Templates Old/Meeting.md", "Templates", false},
		{"TemplatesX.md", "Templates", false},
		{"inbox/Templates/Meeting.md", "Templates", false},
		{"inbox/Templates/Meeting.md", "inbox/Templates", true},
		{"templates/Meeting.md", "Templates", false},
		{"Templates/Meeting.md", "", false},
	}
	for _, tc := range cases {
		if got := isPathInTemplateFolder(tc.path, tc.folder); got != tc.want {
			t.Errorf("isPathInTemplateFolder(%q, %q) = %v, want %v", tc.path, tc.folder, got, tc.want)
		}
	}
}

func TestRewriteTemplateFolderForRename(t *testing.T) {
	folder := func(f string) *TemplateFolderSettings { return &TemplateFolderSettings{Folder: f} }
	if got := rewriteTemplateFolderForRename(folder("Templates"), "Templates", "Blueprints"); got.Folder != "Blueprints" {
		t.Errorf("renamed folder = %q", got.Folder)
	}
	if got := rewriteTemplateFolderForRename(folder("Meta/Templates"), "Meta", "Archive/Meta"); got.Folder != "Archive/Meta/Templates" {
		t.Errorf("moved parent = %q", got.Folder)
	}
	same := folder("Templates")
	for _, rename := range [][2]string{{"Templates Old", "Old"}, {"Templates/Work", "Templates/Jobs"}, {"Templates", "Templates"}} {
		if got := rewriteTemplateFolderForRename(same, rename[0], rename[1]); got != same {
			t.Errorf("rename %v changed the setting to %+v", rename, got)
		}
	}
	if got := rewriteTemplateFolderForRename(nil, "Templates", "X"); got != nil {
		t.Errorf("nil settings came back as %+v", got)
	}
}

// tasksFolderListsForSettings' cases: the template folder joins the excluded
// list once, wins inside a Task folder, and an unusable one adds nothing.
func TestTasksFolderListsAddTheTemplateFolder(t *testing.T) {
	excluded, included := tasksFolderLists(VaultSettings{
		Tasks:     &TasksSettings{ExcludedFolders: []string{"inbox/Books"}},
		Templates: &TemplateFolderSettings{Folder: "Templates"},
	})
	if !reflect.DeepEqual(excluded, []string{"inbox/Books", "Templates"}) || len(included) != 0 {
		t.Fatalf("lists = %v / %v", excluded, included)
	}
	if !isPathExcludedFromTasks("Templates/Work/1on1.md", excluded, included) {
		t.Error("a nested template note feeds Tasks")
	}

	excluded, included = tasksFolderLists(VaultSettings{
		Tasks:     &TasksSettings{IncludedFolders: []string{"Work"}},
		Templates: &TemplateFolderSettings{Folder: "Work/Templates"},
	})
	if isPathExcludedFromTasks("Work/Plan.md", excluded, included) ||
		!isPathExcludedFromTasks("Work/Templates/Plan.md", excluded, included) {
		t.Errorf("template folder inside a Task folder: %v / %v", excluded, included)
	}

	excluded, _ = tasksFolderLists(VaultSettings{
		Tasks:     &TasksSettings{ExcludedFolders: []string{"Templates"}},
		Templates: &TemplateFolderSettings{Folder: "Templates"},
	})
	if !reflect.DeepEqual(excluded, []string{"Templates"}) {
		t.Errorf("listed twice: %v", excluded)
	}
	excluded, _ = tasksFolderLists(VaultSettings{Templates: &TemplateFolderSettings{Folder: ".zennotes/templates"}})
	if len(excluded) != 0 {
		t.Errorf("an unusable folder was excluded: %v", excluded)
	}
}

// What vault.json can hold under `templates`, decoded, normalized and written
// back, against JSON.stringify(normalizeTemplateFolderSettings(JSON.parse(raw))).
// A value the TypeScript side reads as absent must not fail the whole file.
func TestTemplateFolderJSONMatchesTypeScript(t *testing.T) {
	cases := []struct {
		name, raw, want string
	}{
		{"absent", `{}`, ``},
		{"folder", `{"templates":{"folder":"Templates"}}`, `{"folder":"Templates"}`},
		{"cleaned", `{"templates":{"folder":" /Templates/ "}}`, `{"folder":"Templates"}`},
		{"empty", `{"templates":{"folder":""}}`, ``},
		{"traversal", `{"templates":{"folder":"../x"}}`, ``},
		{"hidden", `{"templates":{"folder":".zennotes"}}`, ``},
		{"not a string", `{"templates":{"folder":42}}`, ``},
		{"not an object", `{"templates":"Templates"}`, ``},
		{"an array", `{"templates":["Templates"]}`, ``},
		{"null", `{"templates":null}`, ``},
		{"keys match exactly", `{"templates":{"Folder":"Templates"}}`, ``},
		{"a stray key is dropped", `{"templates":{"folder":"Templates","extra":1}}`, `{"folder":"Templates"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var settings VaultSettings
			if err := json.Unmarshal([]byte(tc.raw), &settings); err != nil {
				t.Fatalf("decode failed the whole file: %v", err)
			}
			normalized := normalizeVaultSettings(settings, PrimaryNotesInbox)
			out, err := json.Marshal(normalized)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(out, &fields); err != nil {
				t.Fatal(err)
			}
			if got := string(fields["templates"]); got != tc.want {
				t.Errorf("templates = %s, want %s", got, tc.want)
			}
		})
	}
}

func newTemplateFolderVault(t *testing.T, settings string, files map[string]string) *Vault {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".zennotes/vault.json", settings)
	for rel, body := range files {
		write(rel, body)
	}
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// #1024 end to end: SetSettings keeps the folder (GetSettings, cache and
// cloneSettings included), the task scan and the single-note rescan skip it,
// and IncludeExcluded reaches it again.
func TestTemplateFolderStaysOutOfTasks(t *testing.T) {
	v := newTemplateFolderVault(t, `{"primaryNotesLocation":"root"}`, map[string]string{
		"Templates/Meeting.md":    "# {{title}}\n\n- [ ] agenda item\n",
		"Templates/Work/Retro.md": "- [ ] action item\n",
		"Notes/Plan.md":           "- [ ] real work\n",
	})
	settings, err := v.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	settings.Templates = &TemplateFolderSettings{Folder: " Templates/ "}
	saved, err := v.SetSettings(settings)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Templates == nil || saved.Templates.Folder != "Templates" {
		t.Fatalf("SetSettings handed back %+v", saved.Templates)
	}
	read, err := v.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if read.Templates == nil || read.Templates.Folder != "Templates" {
		t.Fatalf("GetSettings dropped the template folder: %+v", read.Templates)
	}

	contents := func(tasks []Task) []string {
		out := []string{}
		for _, tk := range tasks {
			out = append(out, tk.Content)
		}
		sort.Strings(out)
		return out
	}
	tasks, err := v.ScanTasks()
	if err != nil {
		t.Fatal(err)
	}
	if got := contents(tasks); !reflect.DeepEqual(got, []string{"real work"}) {
		t.Errorf("ScanTasks = %v", got)
	}
	for rel, want := range map[string]int{"Templates/Meeting.md": 0, "Templates/Work/Retro.md": 0, "Notes/Plan.md": 1} {
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
	if got := contents(all); !reflect.DeepEqual(got, []string{"action item", "agenda item", "real work"}) {
		t.Errorf("IncludeExcluded = %v", got)
	}

	// Cleared, the template notes count again.
	read.Templates = nil
	if _, err := v.SetSettings(read); err != nil {
		t.Fatal(err)
	}
	tasks, err = v.ScanTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 {
		t.Errorf("after clearing, ScanTasks = %v", contents(tasks))
	}
}

func TestTemplateFolderStaysOutOfTextSearch(t *testing.T) {
	v := newTemplateFolderVault(t, `{"primaryNotesLocation":"root","templates":{"folder":"Templates"}}`, map[string]string{
		"Templates/Meeting.md": "## zqxagenda item\n",
		"Notes/Sync.md":        "## zqxagenda item\n",
	})
	matches, err := v.SearchText("zqxagenda")
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{}
	for _, m := range matches {
		paths = append(paths, m.Path)
	}
	if !reflect.DeepEqual(paths, []string{"Notes/Sync.md"}) {
		t.Errorf("SearchText = %v", paths)
	}

	// The cached candidates still hold the template note: clearing the
	// setting brings it back without a rescan of the vault.
	settings, err := v.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	settings.Templates = nil
	if _, err := v.SetSettings(settings); err != nil {
		t.Fatal(err)
	}
	matches, err = v.SearchText("zqxagenda")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Errorf("after clearing, SearchText = %+v", matches)
	}
}

func TestRenameFolderMovesTheTemplateFolder(t *testing.T) {
	v := newTemplateFolderVault(t, `{"templates":{"folder":"inbox/Templates"}}`, map[string]string{
		"inbox/Templates/Meeting.md": "# {{title}}\n",
	})
	if _, err := v.RenameFolder(FolderInbox, "Templates", "Blueprints"); err != nil {
		t.Fatal(err)
	}
	settings, err := v.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.Templates == nil || settings.Templates.Folder != "inbox/Blueprints" {
		t.Errorf("template folder after rename = %+v", settings.Templates)
	}
}
