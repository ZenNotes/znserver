package vault

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVaultDefaultModesAreTight(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix file modes")
	}
	root := t.TempDir()
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.WriteNote("hello.md", "hi"); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Join(v.Root(), "hello.md"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("note perm = %o, want 0600", perm)
	}

	// Note files live under inbox/, but the directory was created during
	// EnsureLayout. Inspect the inbox dir to verify dirMode applied.
	dirInfo, err := os.Stat(filepath.Join(v.Root(), "inbox"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("inbox dir perm = %o, want 0700", perm)
	}
}

func TestVaultModeOverride(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix file modes")
	}
	root := t.TempDir()
	v, err := New(root, Options{FileMode: 0o644, DirMode: 0o755})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.WriteNote("hello.md", "hi"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(v.Root(), "hello.md"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Fatalf("override perm = %o, want 0644", perm)
	}
}

func TestImportAssetEnforcesMaxBytes(t *testing.T) {
	root := t.TempDir()
	v, err := New(root, Options{MaxAssetBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("a"), 17)
	_, err = v.ImportAsset("note.md", "x.bin", bytes.NewReader(big))
	if !errors.Is(err, ErrAssetTooLarge) {
		t.Fatalf("expected ErrAssetTooLarge, got %v", err)
	}
	// Partial file should be removed.
	entries, _ := os.ReadDir(v.Root())
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".bin") {
			t.Fatalf("partial asset %q should be cleaned up", e.Name())
		}
	}
}

func TestImportAssetWithinLimit(t *testing.T) {
	root := t.TempDir()
	v, err := New(root, Options{MaxAssetBytes: 32})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("a"), 16)
	asset, err := v.ImportAsset("note.md", "x.bin", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	abs := filepath.Join(v.Root(), asset.Name)
	got, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("written bytes differ from input")
	}
}

func TestImportAssetReportsAtBoundary(t *testing.T) {
	root := t.TempDir()
	v, err := New(root, Options{MaxAssetBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("a"), 8)
	if _, err := v.ImportAsset("note.md", "x.bin", bytes.NewReader(body)); err != nil {
		t.Fatalf("8/8 bytes should succeed, got %v", err)
	}
	// 9-byte body must be rejected even though only one byte over.
	body9 := bytes.Repeat([]byte("a"), 9)
	if _, err := v.ImportAsset("note.md", "y.bin", bytes.NewReader(body9)); !errors.Is(err, ErrAssetTooLarge) {
		t.Fatalf("9/8 should reject with ErrAssetTooLarge, got %v", err)
	}
}

func TestNoteCommentsFollowRenameDuplicateAndDelete(t *testing.T) {
	root := t.TempDir()
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	meta, err := v.WriteNote("inbox/Alpha.md", "hello world")
	if err != nil {
		t.Fatal(err)
	}
	comments, err := v.WriteNoteComments(meta.Path, []NoteComment{{
		AnchorStart: 0,
		AnchorEnd:   5,
		AnchorText:  "hello",
		Body:        "Tighten this claim.",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0].ID == "" {
		t.Fatalf("comment was not normalized: %#v", comments)
	}

	renamed, err := v.RenameNote(meta.Path, "Beta")
	if err != nil {
		t.Fatal(err)
	}
	oldComments, err := v.ReadNoteComments(meta.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(oldComments) != 0 {
		t.Fatalf("old sidecar still has comments: %#v", oldComments)
	}
	renamedComments, err := v.ReadNoteComments(renamed.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(renamedComments) != 1 || renamedComments[0].NotePath != renamed.Path {
		t.Fatalf("comments did not follow rename: %#v", renamedComments)
	}

	duplicated, err := v.DuplicateNote(renamed.Path)
	if err != nil {
		t.Fatal(err)
	}
	duplicatedComments, err := v.ReadNoteComments(duplicated.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(duplicatedComments) != 1 || duplicatedComments[0].NotePath != duplicated.Path {
		t.Fatalf("comments did not copy to duplicate: %#v", duplicatedComments)
	}
	if duplicatedComments[0].ID == renamedComments[0].ID {
		t.Fatalf("duplicated note should get independent comment ids")
	}

	if err := v.DeleteNote(renamed.Path); err != nil {
		t.Fatal(err)
	}
	deletedComments, err := v.ReadNoteComments(renamed.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(deletedComments) != 0 {
		t.Fatalf("comments should be removed with deleted note: %#v", deletedComments)
	}
}

func TestReadNoteRefusesSymlinkOutsideVault(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("classified"), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(v.Root(), "evil.md")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}

	if _, err := v.ReadNote("evil.md"); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("expected ErrPathEscape via ReadNote, got %v", err)
	}
}

func TestWriteNoteRefusesSymlinkOutsideVault(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(v.Root(), "evil.md")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if _, err := v.WriteNote("evil.md", "tampered"); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("expected ErrPathEscape via WriteNote, got %v", err)
	}
	// The target file outside the vault must not be touched.
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Fatalf("file outside vault was modified: %q", got)
	}
}

func TestDuplicateFolderRefusesNestedSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.md")
	if err := os.WriteFile(secret, []byte("classified"), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(v.Root(), string(FolderInbox), "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "safe.md"), []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(source, "leak.md")); err != nil {
		t.Fatal(err)
	}

	if _, err := v.DuplicateFolder(FolderInbox, "source"); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("expected ErrPathEscape duplicating folder with symlink, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(v.Root(), string(FolderInbox), "source copy", "leak.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink target should not be copied into duplicated folder, stat err=%v", err)
	}
}

// Compile-time assertion that ImportAsset accepts an io.Reader (silences
// unused-import lints if the asset tests are stripped down later).
var _ = io.Reader(bytes.NewReader(nil))
