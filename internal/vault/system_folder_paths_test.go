package vault

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// remapVault returns a vault whose system folders live at custom paths.
func remapVault(t *testing.T, paths map[string]string) *Vault {
	t.Helper()
	v, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.SetSettings(VaultSettings{
		PrimaryNotesLocation: PrimaryNotesInbox,
		SystemFolderPaths:    paths,
	}); err != nil {
		t.Fatal(err)
	}
	settings, err := v.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	for folder, want := range paths {
		if got := settings.SystemFolderPaths[folder]; got != want {
			t.Fatalf("settings.systemFolderPaths[%s] = %q, want %q", folder, got, want)
		}
	}
	return v
}

func TestRemappedInboxIsReadAndWrittenInTheCustomDirectory(t *testing.T) {
	v := remapVault(t, map[string]string{"inbox": "bucket"})

	if _, err := os.Stat(filepath.Join(v.Root(), "bucket")); err != nil {
		t.Fatalf("remapped inbox directory was not created: %v", err)
	}

	meta, err := v.CreateNote(FolderInbox, "Remapped", "")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Path != "bucket/Remapped.md" {
		t.Fatalf("created note path = %q, want bucket/Remapped.md", meta.Path)
	}
	if _, err := os.Stat(filepath.Join(v.Root(), "bucket", "Remapped.md")); err != nil {
		t.Fatalf("note did not land in the remapped inbox: %v", err)
	}
	if _, err := os.Stat(filepath.Join(v.Root(), "inbox", "Remapped.md")); err == nil {
		t.Fatal("note was written to the default inbox directory")
	}

	read, err := v.ReadNote("bucket/Remapped.md")
	if err != nil {
		t.Fatal(err)
	}
	if read.Folder != FolderInbox {
		t.Fatalf("read note folder = %q, want inbox", read.Folder)
	}

	notes, err := v.ListNotes()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, note := range notes {
		if note.Path == "bucket/Remapped.md" {
			found = true
			if note.Folder != FolderInbox {
				t.Fatalf("listed note folder = %q, want inbox", note.Folder)
			}
		}
	}
	if !found {
		t.Fatalf("remapped inbox note missing from ListNotes: %+v", notes)
	}
}

func TestRemappedTrashRoundTripKeepsTheSubfolder(t *testing.T) {
	v := remapVault(t, map[string]string{"inbox": "bucket", "trash": "deleted"})

	created, err := v.CreateNote(FolderInbox, "Doomed", "Projects")
	if err != nil {
		t.Fatal(err)
	}
	if created.Path != "bucket/Projects/Doomed.md" {
		t.Fatalf("created note path = %q, want bucket/Projects/Doomed.md", created.Path)
	}

	trashed, err := v.MoveToTrash(created.Path)
	if err != nil {
		t.Fatal(err)
	}
	if trashed.Path != "deleted/Projects/Doomed.md" || trashed.Folder != FolderTrash {
		t.Fatalf("trashed note = {%q %q}, want {deleted/Projects/Doomed.md trash}", trashed.Path, trashed.Folder)
	}

	restored, err := v.RestoreFromTrash(trashed.Path)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Path != "bucket/Projects/Doomed.md" || restored.Folder != FolderInbox {
		t.Fatalf("restored note = {%q %q}, want {bucket/Projects/Doomed.md inbox}", restored.Path, restored.Folder)
	}
}

func TestPrimaryRootListsADefaultNameThatWasRemappedAway(t *testing.T) {
	v, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.SetSettings(VaultSettings{
		PrimaryNotesLocation: PrimaryNotesRoot,
		SystemFolderPaths:    map[string]string{"quick": "Fast"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := v.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	// `quick/` is an ordinary user folder now that quick lives in `Fast/`, so
	// the root walk must stop skipping it.
	write := func(rel string) {
		t.Helper()
		abs := filepath.Join(v.Root(), filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte("body"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("quick/User.md")
	write("Fast/Scratch.md")

	notes, err := v.ListNotes()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]NoteFolder{}
	for _, note := range notes {
		got[note.Path] = note.Folder
	}
	if folder, ok := got["quick/User.md"]; !ok || folder != FolderInbox {
		t.Fatalf("quick/User.md = (%q, %v), want (inbox, true); listing: %v", folder, ok, got)
	}
	if folder, ok := got["Fast/Scratch.md"]; !ok || folder != FolderQuick {
		t.Fatalf("Fast/Scratch.md = (%q, %v), want (quick, true); listing: %v", folder, ok, got)
	}
}

func TestSettingsRoundTripKeepsSystemFolderPaths(t *testing.T) {
	v := remapVault(t, map[string]string{"archive": "cold-storage"})

	written, err := v.SetSettings(VaultSettings{
		PrimaryNotesLocation: PrimaryNotesInbox,
		SystemFolderPaths:    map[string]string{"archive": "cold-storage"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// SetSettings' return value is what the HTTP layer echoes to the client.
	if got := written.SystemFolderPaths["archive"]; got != "cold-storage" {
		t.Fatalf("SetSettings returned systemFolderPaths[archive] = %q, want cold-storage", got)
	}
}

func TestGetSettingsCachesUntilTheFileChanges(t *testing.T) {
	v := remapVault(t, map[string]string{"inbox": "bucket"})

	// White-box: poison the cached copy. A second read that still reports the
	// poisoned value proves the file was not re-read and re-parsed.
	v.settingsMu.Lock()
	if v.settingsCache == nil {
		v.settingsMu.Unlock()
		t.Fatal("settings were not cached after a read")
	}
	v.settingsCache.settings.SystemFolderPaths["inbox"] = "sentinel"
	v.settingsMu.Unlock()

	cached, err := v.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if cached.SystemFolderPaths["inbox"] != "sentinel" {
		t.Fatal("GetSettings re-parsed vault.json even though it had not changed")
	}

	// The caller gets a copy, so mutating it cannot corrupt the cache.
	cached.SystemFolderPaths["inbox"] = "mutated"
	again, err := v.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if again.SystemFolderPaths["inbox"] != "sentinel" {
		t.Fatal("mutating the returned settings leaked into the cache")
	}

	// A vault.json written behind the vault's back is picked up: the mtime and
	// size no longer match the cached key.
	raw, err := json.Marshal(VaultSettings{
		PrimaryNotesLocation: PrimaryNotesInbox,
		SystemFolderPaths:    map[string]string{"inbox": "elsewhere", "trash": "deleted"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(v.settingsPath(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	fresh, err := v.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if fresh.SystemFolderPaths["inbox"] != "elsewhere" || fresh.SystemFolderPaths["trash"] != "deleted" {
		t.Fatalf("edited vault.json was not picked up: %+v", fresh.SystemFolderPaths)
	}
}

func TestFolderForRelativePathPrefersOverridesOverDefaultNames(t *testing.T) {
	cases := []struct {
		name  string
		rel   string
		paths map[string]string
		want  NoteFolder
		ok    bool
	}{
		{
			name: "default names classify with no overrides",
			rel:  "archive/Note.md",
			want: FolderArchive,
			ok:   true,
		},
		{
			name:  "remapped folder classifies by its custom path",
			rel:   "deleted/Note.md",
			paths: map[string]string{"trash": "deleted"},
			want:  FolderTrash,
			ok:    true,
		},
		{
			name:  "a default name left behind is a user folder",
			rel:   "trash/Note.md",
			paths: map[string]string{"trash": "deleted"},
			want:  FolderInbox,
			ok:    true,
		},
		{
			name:  "a remapped-away inbox name is a user folder",
			rel:   "inbox/Note.md",
			paths: map[string]string{"inbox": "bucket"},
			want:  FolderInbox,
			ok:    true,
		},
		{
			name:  "a swap classifies by location, not by name",
			rel:   "archive/Note.md",
			paths: map[string]string{"inbox": "archive", "archive": "bucket"},
			want:  FolderInbox,
			ok:    true,
		},
		{
			name:  "the swapped-out folder classifies too",
			rel:   "bucket/Note.md",
			paths: map[string]string{"inbox": "archive", "archive": "bucket"},
			want:  FolderArchive,
			ok:    true,
		},
		{
			// The on-disk case is whatever the directory was created with, so
			// classification is case-insensitive like the TS side. (#186)
			name: "a default name classifies whatever its case",
			rel:  "Archive/Note.md",
			want: FolderArchive,
			ok:   true,
		},
		{
			name:  "a custom path classifies whatever its case",
			rel:   "DELETED/Note.md",
			paths: map[string]string{"trash": "deleted"},
			want:  FolderTrash,
			ok:    true,
		},
		{
			name: "reserved root names stay unclassified",
			rel:  "assets/image.png",
			ok:   false,
		},
		{
			name:  "reserved root names stay unclassified with overrides too",
			rel:   "attachements/image.png",
			paths: map[string]string{"inbox": "bucket"},
			ok:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			folder, ok := FolderForRelativePathWithSettings(tc.rel, tc.paths)
			if ok != tc.ok || (tc.ok && folder != tc.want) {
				t.Fatalf("FolderForRelativePathWithSettings(%q, %v) = (%q, %v), want (%q, %v)",
					tc.rel, tc.paths, folder, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestNormalizeSystemFolderPathsRejectsCollisions(t *testing.T) {
	cases := []struct {
		name string
		raw  map[string]string
		want map[string]string
	}{
		{
			name: "a swap collapses to no overrides at all",
			raw:  map[string]string{"inbox": "archive", "archive": "inbox"},
			want: nil,
		},
		{
			name: "taking another folder's default name is rejected",
			raw:  map[string]string{"inbox": "trash"},
			want: nil,
		},
		{
			name: "taking another folder's default name is rejected case-insensitively",
			raw:  map[string]string{"inbox": "Trash"},
			want: nil,
		},
		{
			name: "the rejection is per entry",
			raw:  map[string]string{"inbox": "archive", "trash": "deleted"},
			want: map[string]string{"trash": "deleted"},
		},
		{
			// Which of the two survives is the sweep order (inbox, quick,
			// archive, trash), the same order the TS normalizer uses.
			name: "two folders may not share one custom path",
			raw:  map[string]string{"quick": "shared", "trash": "shared"},
			want: map[string]string{"trash": "shared"},
		},
		{
			name: "a three-way rotation collapses too",
			raw:  map[string]string{"inbox": "quick", "quick": "archive", "archive": "inbox"},
			want: nil,
		},
		{
			name: "a valid override survives its rejected swap partners",
			raw:  map[string]string{"inbox": "archive", "archive": "inbox", "trash": "deleted"},
			want: map[string]string{"trash": "deleted"},
		},
		{
			// Only the OTHER folders' defaults are off limits; recasing your own
			// is a real rename on a case-preserving filesystem.
			name: "a folder may recase its own default name",
			raw:  map[string]string{"archive": "Archive"},
			want: map[string]string{"archive": "Archive"},
		},
		{
			name: "reserved names are rejected",
			raw:  map[string]string{"trash": "assets"},
			want: nil,
		},
		{
			name: "reserved names are rejected case-insensitively",
			raw:  map[string]string{"trash": "Comments"},
			want: nil,
		},
		{
			name: "a folder's own default name is dropped as a no-op",
			raw:  map[string]string{"trash": "trash"},
			want: nil,
		},
		{
			name: "independent custom paths survive",
			raw:  map[string]string{"inbox": "bucket", "trash": "deleted"},
			want: map[string]string{"inbox": "bucket", "trash": "deleted"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeSystemFolderPaths(tc.raw)
			if len(got) != len(tc.want) {
				t.Fatalf("NormalizeSystemFolderPaths(%v) = %v, want %v", tc.raw, got, tc.want)
			}
			for key, want := range tc.want {
				if got[key] != want {
					t.Fatalf("NormalizeSystemFolderPaths(%v) = %v, want %v", tc.raw, got, tc.want)
				}
			}
		})
	}
}
