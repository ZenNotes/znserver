package vault

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeleteAssetMovesIntoStoreAndListsBack(t *testing.T) {
	root := t.TempDir()
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	writeAsset(t, root, "assets/pic.png", "PNG")

	deleted, err := v.DeleteAsset("assets/pic.png")
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Path != "assets/pic.png" || deleted.Name != "pic.png" {
		t.Fatalf("deleted = %+v, want original path+name", deleted)
	}
	if !deletedAssetTokenRe.MatchString(deleted.UndoToken) {
		t.Fatalf("undo token %q does not match the desktop token shape", deleted.UndoToken)
	}
	if _, err := os.Stat(filepath.Join(root, "assets", "pic.png")); !os.IsNotExist(err) {
		t.Fatal("original file still present after delete")
	}
	stored := filepath.Join(root, internalVaultDir, deletedAssetsDir, deleted.UndoToken, "pic.png")
	if _, err := os.Stat(stored); err != nil {
		t.Fatalf("stored file missing: %v", err)
	}

	listed, err := v.ListDeletedAssets()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].UndoToken != deleted.UndoToken || listed[0].Path != "assets/pic.png" {
		t.Fatalf("listed = %+v, want the deleted entry", listed)
	}
}

func TestRestoreDeletedAssetReturnsToOriginalFolderAndDedupes(t *testing.T) {
	root := t.TempDir()
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	writeAsset(t, root, "docs/report.pdf", "PDF-1")
	deleted, err := v.DeleteAsset("docs/report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	// Something new takes the original name before the restore.
	writeAsset(t, root, "docs/report.pdf", "PDF-2")

	meta, err := v.RestoreDeletedAsset(deleted)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Path != "docs/report 2.pdf" {
		t.Fatalf("restored path = %q, want the deduped docs/report 2.pdf", meta.Path)
	}
	body, err := os.ReadFile(filepath.Join(root, "docs", "report 2.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "PDF-1" {
		t.Fatalf("restored body = %q, want the deleted bytes", body)
	}
	// The store entry is consumed by the restore.
	if listed, _ := v.ListDeletedAssets(); len(listed) != 0 {
		t.Fatalf("store still lists %d entries after restore", len(listed))
	}
}

func TestRestoreDeletedAssetRejectsBadInput(t *testing.T) {
	root := t.TempDir()
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	base := DeletedAsset{Path: "assets/x.png", Name: "x.png", UndoToken: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}

	bad := base
	bad.UndoToken = "../../../etc"
	if _, err := v.RestoreDeletedAsset(bad); err == nil {
		t.Fatal("path-traversal token accepted")
	}
	bad = base
	bad.Path = ".zennotes/vault.json"
	if _, err := v.RestoreDeletedAsset(bad); err == nil {
		t.Fatal("internal path accepted")
	}
	bad = base
	bad.Path = "notes/Note.md"
	if _, err := v.RestoreDeletedAsset(bad); err == nil {
		t.Fatal("markdown path accepted")
	}
	bad = base
	bad.Name = "../vault.json"
	if _, err := v.RestoreDeletedAsset(bad); err == nil {
		t.Fatal("path-escaping name accepted")
	}
}

func TestRestoreDeletedAssetFollowsStoredMetadataNotTheRequest(t *testing.T) {
	root := t.TempDir()
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	writeAsset(t, root, "assets/pic.png", "PNG")
	deleted, err := v.DeleteAsset("assets/pic.png")
	if err != nil {
		t.Fatal(err)
	}

	// A hostile request keeps a valid token but names the metadata file as the
	// thing to restore. Trusting it would rename .zn-deleted.json into the
	// vault and then purge the real asset bytes with the trash dir cleanup.
	hostile := deleted
	hostile.Name = deletedAssetMetaFile
	hostile.Path = "assets/x.json"
	meta, err := v.RestoreDeletedAsset(hostile)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Path != "assets/pic.png" {
		t.Fatalf("restored path = %q, want the stored assets/pic.png", meta.Path)
	}
	body, err := os.ReadFile(filepath.Join(root, "assets", "pic.png"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "PNG" {
		t.Fatalf("restored body = %q, want the original asset bytes", body)
	}
}

func TestPurgeAndEmptyDeletedAssets(t *testing.T) {
	root := t.TempDir()
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	writeAsset(t, root, "a.png", "A")
	writeAsset(t, root, "b.png", "B")
	delA, err := v.DeleteAsset("a.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.DeleteAsset("b.png"); err != nil {
		t.Fatal(err)
	}

	if err := v.PurgeDeletedAsset("not-a-token"); err == nil {
		t.Fatal("invalid token accepted by purge")
	}
	if err := v.PurgeDeletedAsset(delA.UndoToken); err != nil {
		t.Fatal(err)
	}
	listed, err := v.ListDeletedAssets()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Name != "b.png" {
		t.Fatalf("after purge listed = %+v, want only b.png", listed)
	}

	if err := v.EmptyDeletedAssets(); err != nil {
		t.Fatal(err)
	}
	if listed, _ := v.ListDeletedAssets(); len(listed) != 0 {
		t.Fatalf("after empty listed = %+v, want none", listed)
	}
}

func TestDeleteAssetRefusesNotesAndInternalFiles(t *testing.T) {
	root := t.TempDir()
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	writeAsset(t, root, "inbox/Note.md", "note")
	if _, err := v.DeleteAsset("inbox/Note.md"); err == nil {
		t.Fatal("markdown note accepted by asset delete")
	}
	if _, err := v.DeleteAsset(".zennotes/vault.json"); err == nil {
		t.Fatal("internal file accepted by asset delete")
	}
}

func TestDuplicateAssetCopiesNextToSource(t *testing.T) {
	root := t.TempDir()
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	writeAsset(t, root, "assets/pic.png", "PNG")

	meta, err := v.DuplicateAsset("assets/pic.png")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Path != "assets/pic copy.png" {
		t.Fatalf("duplicate path = %q, want assets/pic copy.png", meta.Path)
	}
	body, err := os.ReadFile(filepath.Join(root, "assets", "pic copy.png"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "PNG" {
		t.Fatalf("duplicate body = %q", body)
	}
	// A second duplicate dedupes with the shared numbering scheme.
	again, err := v.DuplicateAsset("assets/pic.png")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(again.Path, "pic copy 2.png") {
		t.Fatalf("second duplicate = %q, want pic copy 2.png", again.Path)
	}
}
