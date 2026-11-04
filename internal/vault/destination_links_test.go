package vault

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// A vault with a link from Projects to the Journal folder beside it, which is
// what `zn mcp --allow-path Projects` was walked through.
func linkedVault(t *testing.T) (*Vault, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows")
	}
	root := t.TempDir()
	mustWrite := func(rel, body string) {
		t.Helper()
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(".zennotes/vault.json", `{"primaryNotesLocation":"root"}`)
	mustWrite("Projects/Plan.md", "# Plan\n")
	mustWrite("Projects/Sub/Step.md", "# Step\n")
	mustWrite("Projects/pic.png", "png")
	mustWrite("Journal/Diary.md", "# Diary\n\nprivate\n")
	if err := os.Symlink(filepath.Join("..", "Journal"), filepath.Join(root, "Projects", "inner")); err != nil {
		t.Fatal(err)
	}
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return v, root
}

func journalEntries(t *testing.T, root string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "Journal"))
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func TestDestinationsRefuseAFolderLink(t *testing.T) {
	for name, write := range map[string]func(v *Vault) error{
		"create note": func(v *Vault) error {
			_, err := v.CreateNote(FolderInbox, "Via", "Projects/inner")
			return err
		},
		"create note deeper": func(v *Vault) error {
			_, err := v.CreateNote(FolderInbox, "Via", "Projects/inner/New/Sub")
			return err
		},
		"create drawing": func(v *Vault) error {
			_, err := v.CreateExcalidraw(FolderInbox, "Via", "Projects/inner")
			return err
		},
		"create folder": func(v *Vault) error {
			return v.CreateFolder(FolderInbox, "Projects/inner/Made")
		},
		"create the link's own folder": func(v *Vault) error {
			return v.CreateFolder(FolderInbox, "Projects/inner")
		},
		"move note": func(v *Vault) error {
			_, err := v.MoveNote("Projects/Plan.md", FolderInbox, "Projects/inner")
			return err
		},
		"rename folder under the link": func(v *Vault) error {
			_, err := v.RenameFolder(FolderInbox, "Projects/Sub", "Projects/inner/Sub")
			return err
		},
		"rename folder onto the link": func(v *Vault) error {
			_, err := v.RenameFolder(FolderInbox, "Projects/Sub", "Projects/inner")
			return err
		},
		"move asset": func(v *Vault) error {
			_, err := v.MoveAsset("Projects/pic.png", "Projects/inner")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			v, root := linkedVault(t)
			if err := write(v); !errors.Is(err, ErrThroughSymlink) {
				t.Fatalf("got %v, want ErrThroughSymlink", err)
			}
			if got := journalEntries(t, root); got != "Diary.md" {
				t.Fatalf("Journal holds %s", got)
			}
		})
	}
}

// Archiving mirrors the note's folder into the archive, so a link there is a
// destination too, and so is the inbox when it comes back.
func TestFolderMovesRefuseALinkInTheMirroredPath(t *testing.T) {
	v, root := linkedVault(t)
	if err := os.MkdirAll(filepath.Join(root, "archive", "Held"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("Held", filepath.Join(root, "archive", "Projects")); err != nil {
		t.Fatal(err)
	}
	if _, err := v.ArchiveNote("Projects/Plan.md"); !errors.Is(err, ErrThroughSymlink) {
		t.Fatalf("archive through a link: got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "Projects", "Plan.md")); err != nil {
		t.Fatalf("the refused note moved: %v", err)
	}
}

// What a link is still good for. None of these name a destination through it.
func TestLinksThatKeepWorking(t *testing.T) {
	t.Run("a linked note is read and written through", func(t *testing.T) {
		v, root := linkedVault(t)
		if err := os.Symlink(filepath.Join("..", "Journal", "Diary.md"), filepath.Join(root, "Projects", "alias.md")); err != nil {
			t.Fatal(err)
		}
		if _, err := v.WriteNote("Projects/alias.md", "# Edited\n"); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(root, "Journal", "Diary.md"))
		if err != nil || string(got) != "# Edited\n" {
			t.Fatalf("the link's target holds %q (%v)", got, err)
		}
	})
	t.Run("a file is read through a linked folder", func(t *testing.T) {
		v, _ := linkedVault(t)
		if _, err := v.ReadNote("Projects/inner/Diary.md"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a system folder that is a link takes new notes", func(t *testing.T) {
		root := t.TempDir()
		for _, dir := range []string{"Elsewhere", ".zennotes"} {
			if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(root, ".zennotes", "vault.json"), []byte(`{"primaryNotesLocation":"inbox"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("Elsewhere", filepath.Join(root, "inbox")); err != nil {
			t.Fatal(err)
		}
		v, err := New(root, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := v.CreateNote(FolderInbox, "Linked", "Sub"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(root, "Elsewhere", "Sub", "Linked.md")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a real folder is still created and renamed", func(t *testing.T) {
		v, root := linkedVault(t)
		if err := v.CreateFolder(FolderInbox, "Projects/Fresh/Deeper"); err != nil {
			t.Fatal(err)
		}
		if _, err := v.RenameFolder(FolderInbox, "Projects/Sub", "Projects/Fresh/Sub"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(root, "Projects", "Fresh", "Sub", "Step.md")); err != nil {
			t.Fatal(err)
		}
	})
}

// A client that shows only part of a vault has to know a listed entry is a
// link, because the content belongs to whatever the link names.
func TestListingsMarkLinks(t *testing.T) {
	v, root := linkedVault(t)
	if err := os.Symlink(filepath.Join("..", "Journal", "Diary.md"), filepath.Join(root, "Projects", "alias.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("pic.png", filepath.Join(root, "Projects", "alias.png")); err != nil {
		t.Fatal(err)
	}
	// Twice: the second listing comes from the meta cache.
	for pass := 0; pass < 2; pass++ {
		notes, err := v.ListNotes()
		if err != nil {
			t.Fatal(err)
		}
		flags := map[string]bool{}
		for _, n := range notes {
			flags[n.Path] = n.Symlink
		}
		if linked, listed := flags["Projects/alias.md"]; !listed || !linked {
			t.Errorf("pass %d: alias.md listed=%v symlink=%v", pass, listed, linked)
		}
		if linked, listed := flags["Journal/Diary.md"]; !listed || linked {
			t.Errorf("pass %d: Diary.md listed=%v symlink=%v", pass, listed, linked)
		}
	}
	assets, err := v.ListAssets()
	if err != nil {
		t.Fatal(err)
	}
	flags := map[string]bool{}
	for _, a := range assets {
		flags[a.Path] = a.Symlink
	}
	if linked, listed := flags["Projects/alias.png"]; !listed || !linked {
		t.Errorf("alias.png listed=%v symlink=%v", listed, linked)
	}
	if linked, listed := flags["Projects/pic.png"]; !listed || linked {
		t.Errorf("pic.png listed=%v symlink=%v", listed, linked)
	}
}

// A dangling link looks free to Stat, and a create would write through it.
func TestNewFilesDoNotLandOnADanglingLink(t *testing.T) {
	for name, create := range map[string]func(v *Vault) (string, error){
		"note": func(v *Vault) (string, error) {
			meta, err := v.CreateNote(FolderInbox, "Ghost", "Projects")
			return meta.Path, err
		},
		"drawing": func(v *Vault) (string, error) {
			meta, err := v.CreateExcalidraw(FolderInbox, "Ghost", "Projects")
			return meta.Path, err
		},
		"note copy": func(v *Vault) (string, error) {
			meta, err := v.DuplicateNote("Projects/Ghost source.md")
			return meta.Path, err
		},
		"asset copy": func(v *Vault) (string, error) {
			meta, err := v.DuplicateAsset("Projects/Ghost source.png")
			return meta.Path, err
		},
		"note moved in": func(v *Vault) (string, error) {
			meta, err := v.MoveNote("Other/Ghost.md", FolderInbox, "Projects")
			return meta.Path, err
		},
	} {
		t.Run(name, func(t *testing.T) {
			v, root := linkedVault(t)
			for rel, body := range map[string]string{"Projects/Ghost source.md": "# S\n", "Projects/Ghost source.png": "png", "Other/Ghost.md": "# O\n"} {
				abs := filepath.Join(root, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(abs, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, link := range []string{"Ghost.md", "Ghost.excalidraw", "Ghost source copy.md", "Ghost source copy.png"} {
				if err := os.Symlink(filepath.Join("..", "Journal", "Planted"), filepath.Join(root, "Projects", link)); err != nil {
					t.Fatal(err)
				}
			}
			got, err := create(v)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got, " 2") {
				t.Errorf("got %s, want the next free name", got)
			}
			if entries := journalEntries(t, root); entries != "Diary.md" {
				t.Errorf("Journal holds %s", entries)
			}
		})
	}
}

func TestAFolderCopyDoesNotLandOnADanglingLink(t *testing.T) {
	v, root := linkedVault(t)
	if err := os.Symlink(filepath.Join("..", "Journal", "Planted"), filepath.Join(root, "Projects", "Sub copy")); err != nil {
		t.Fatal(err)
	}
	got, err := v.DuplicateFolder(FolderInbox, "Projects/Sub")
	if err != nil {
		t.Fatal(err)
	}
	if got != "Projects/Sub copy 2" {
		t.Errorf("got %s", got)
	}
	if entries := journalEntries(t, root); entries != "Diary.md" {
		t.Errorf("Journal holds %s", entries)
	}
}

func TestUploadsDoNotGoThroughALink(t *testing.T) {
	t.Run("the assets folder is a link inside the vault", func(t *testing.T) {
		v, root := linkedVault(t)
		if err := os.Symlink("Journal", filepath.Join(root, AssetsDir)); err != nil {
			t.Fatal(err)
		}
		if _, err := v.ImportAsset("", "dropped.png", strings.NewReader("png")); !errors.Is(err, ErrThroughSymlink) {
			t.Fatalf("got %v", err)
		}
		if entries := journalEntries(t, root); entries != "Diary.md" {
			t.Errorf("Journal holds %s", entries)
		}
	})
	t.Run("the assets folder is a link out of the vault", func(t *testing.T) {
		v, root := linkedVault(t)
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(root, AssetsDir)); err != nil {
			t.Fatal(err)
		}
		if _, err := v.ImportAsset("", "dropped.png", strings.NewReader("png")); !errors.Is(err, ErrPathEscape) {
			t.Fatalf("got %v", err)
		}
		if entries, _ := os.ReadDir(outside); len(entries) != 0 {
			t.Errorf("outside holds %v", entries)
		}
	})
	t.Run("a dangling link holds the name", func(t *testing.T) {
		v, root := linkedVault(t)
		if err := os.MkdirAll(filepath.Join(root, AssetsDir), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("..", "Journal", "dropped.png"), filepath.Join(root, AssetsDir, "dropped.png")); err != nil {
			t.Fatal(err)
		}
		got, err := v.ImportAsset("", "dropped.png", strings.NewReader("png"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got.Path, "dropped 2.png") {
			t.Errorf("got %s", got.Path)
		}
		if entries := journalEntries(t, root); entries != "Diary.md" {
			t.Errorf("Journal holds %s", entries)
		}
	})
}

func TestInfoNamesLinkedSystemFolders(t *testing.T) {
	v, root := linkedVault(t)
	if got := v.Info().LinkedSystemFolders; len(got) != 0 {
		t.Fatalf("no system folder is a link, got %v", got)
	}
	if err := os.RemoveAll(filepath.Join(root, "quick")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("Journal", filepath.Join(root, "quick")); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(v.Info().LinkedSystemFolders, ","); got != "quick" {
		t.Fatalf("got %q", got)
	}
	settings, err := v.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	settings.SystemFolderPaths = map[string]string{"archive": "Held"}
	if _, err := v.SetSettings(settings); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "Held")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("Journal", filepath.Join(root, "Held")); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(v.Info().LinkedSystemFolders, ","); got != "quick,Held" && got != "Held,quick" {
		t.Fatalf("a remapped folder that is a link: got %q", got)
	}
}

// An empty target means the assets folder, which is no less a destination.
func TestTheDefaultAssetMoveDoesNotGoThroughALink(t *testing.T) {
	v, root := linkedVault(t)
	if err := os.RemoveAll(filepath.Join(root, AssetsDir)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("Journal", filepath.Join(root, AssetsDir)); err != nil {
		t.Fatal(err)
	}
	if _, err := v.MoveAsset("Projects/pic.png", ""); !errors.Is(err, ErrThroughSymlink) {
		t.Fatalf("got %v", err)
	}
	if entries := journalEntries(t, root); entries != "Diary.md" {
		t.Errorf("Journal holds %s", entries)
	}
}

func TestTheDefaultAssetMoveStillWorks(t *testing.T) {
	v, root := linkedVault(t)
	if _, err := v.MoveAsset("Projects/pic.png", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, AssetsDir, "pic.png")); err != nil {
		t.Error(err)
	}
}

// A system folder that is a link is hidden at the vault root, and hiding it must
// not cost the root entries that sort after it.
func TestALinkedSystemFolderKeepsTheRootListed(t *testing.T) {
	for name, tc := range map[string]struct {
		paths map[string]string
		entry string
	}{
		"quick":          {entry: "quick"},
		"archive":        {entry: "archive"},
		"trash":          {entry: "trash"},
		"assets":         {entry: AssetsDir},
		"remapped":       {paths: map[string]string{"archive": "Held"}, entry: "Held"},
		"remapped first": {paths: map[string]string{"trash": "Aside"}, entry: "Aside"},
	} {
		t.Run(name, func(t *testing.T) {
			v, root := linkedVault(t)
			if tc.paths != nil {
				settings, err := v.GetSettings()
				if err != nil {
					t.Fatal(err)
				}
				settings.SystemFolderPaths = tc.paths
				if _, err := v.SetSettings(settings); err != nil {
					t.Fatal(err)
				}
			}
			for rel, body := range map[string]string{
				"000.md":      "# First\n",
				"zebra.md":    "# Zebra\n",
				"zoo/Deep.md": "# Deep\n",
			} {
				abs := filepath.Join(root, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(abs, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			link := filepath.Join(root, tc.entry)
			if err := os.RemoveAll(link); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("Journal", link); err != nil {
				t.Fatal(err)
			}

			notes, err := v.ListNotes()
			if err != nil {
				t.Fatal(err)
			}
			listed := map[string]bool{}
			for _, n := range notes {
				listed[n.Path] = true
				if strings.HasPrefix(n.Path, tc.entry+"/") {
					t.Errorf("the linked folder is listed: %s", n.Path)
				}
			}
			for _, want := range []string{"000.md", "Journal/Diary.md", "Projects/Plan.md", "zebra.md", "zoo/Deep.md"} {
				if !listed[want] {
					t.Errorf("%s is missing from %v", want, listed)
				}
			}
		})
	}
}

// The same holds for a plain file that happens to carry a hidden name.
func TestAFileNamedLikeASystemFolderKeepsTheRootListed(t *testing.T) {
	v, root := linkedVault(t)
	if err := os.RemoveAll(filepath.Join(root, "quick")); err != nil {
		t.Fatal(err)
	}
	for rel, body := range map[string]string{"quick": "not a folder", "zebra.md": "# Zebra\n"} {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	notes, err := v.ListNotes()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range notes {
		if n.Path == "zebra.md" {
			return
		}
	}
	t.Errorf("zebra.md is missing from %+v", notes)
}

// A system folder may carry a name that looks like a note. A link there is still
// the system folder, so the walk of the root must not take it for an inbox note.
func TestALinkedSystemFolderNamedLikeANoteIsNotAnInboxNote(t *testing.T) {
	v, root := linkedVault(t)
	settings, err := v.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	settings.SystemFolderPaths = map[string]string{"archive": "Held.md"}
	if _, err := v.SetSettings(settings); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "Held.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("Journal", "Diary.md"), filepath.Join(root, "Held.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "zebra.md"), []byte("# Zebra\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	notes, err := v.ListNotes()
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, n := range notes {
		listed[n.Path] = true
		if n.Path == "Held.md" && n.Folder == FolderInbox {
			t.Errorf("the linked system folder is listed as an inbox note: %+v", n)
		}
	}
	if !listed["zebra.md"] {
		t.Errorf("zebra.md is missing from %v", listed)
	}
}
