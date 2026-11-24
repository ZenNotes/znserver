package vault

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Tests for the sync protocol's store, reconcile and mutations (#1108,
// phase 2). The end-to-end test against Cloud's real engine lives in
// integration/cloud-engine.

func newSyncVault(t *testing.T) *Vault {
	t.Helper()
	v, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.SyncVaultID(); err != nil {
		t.Fatal(err)
	}
	return v
}

func reopenSyncVault(t *testing.T, v *Vault, opts Options) *Vault {
	t.Helper()
	v.Close()
	next, err := New(v.Root(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func syncItemAt(t *testing.T, v *Vault, rel string) syncItem {
	t.Helper()
	v.mu.RLock()
	defer v.mu.RUnlock()
	item := v.sync.byPath[rel]
	if item == nil {
		t.Fatalf("no sync item at %s; have %v", rel, syncPaths(v))
	}
	return *item
}

func syncPaths(v *Vault) []string {
	out := []string{}
	for p := range v.sync.byPath {
		out = append(out, p)
	}
	return out
}

func allChanges(t *testing.T, v *Vault, after int64) []SyncChange {
	t.Helper()
	page, err := v.SyncChanges(after, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return page.Data
}

func syncText(body string) *SyncContent {
	data := []byte(body)
	return &SyncContent{Encoding: "utf8", Data: body, Sha256: contentSha256(data), ByteLength: int64(len(data)), MediaType: "text/markdown"}
}

func rev(n int64) *int64 { return &n }

func writeRaw(t *testing.T, v *Vault, rel, body string) {
	t.Helper()
	abs := filepath.Join(v.Root(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSyncStoreStartsFromTheVaultAsItIs(t *testing.T) {
	v, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.WriteNote("inbox/One.md", "# One\n"); err != nil {
		t.Fatal(err)
	}
	writeRaw(t, v, ".zennotes/comments/inbox/One.md.comments.json", `{"version":1,"comments":[]}`)
	writeRaw(t, v, "assets/photo.png", "\x89PNG\x00\x01binary")
	writeRaw(t, v, "node_modules/x.md", "never")
	if v.SyncActive() {
		t.Fatal("a vault nobody syncs has no store")
	}
	if _, err := os.Stat(filepath.Join(v.Root(), ".zennotes", "sync")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no store before first use, stat err = %v", err)
	}

	page, err := v.SyncManifest(1, 250, true)
	if err != nil {
		t.Fatal(err)
	}
	if page.VaultID == "" || page.Cursor != 0 || page.NextPage != nil {
		t.Fatalf("manifest header = %+v", page)
	}
	paths := map[string]SyncManifestItem{}
	for _, item := range page.Data {
		paths[item.Path] = item
	}
	one, ok := paths["inbox/One.md"]
	if !ok || one.Revision != 1 || one.Kind != "text" || one.MediaType != "text/markdown" ||
		one.Content == nil || one.Content.Data != "# One\n" || one.Sha256 != contentSha256([]byte("# One\n")) {
		t.Fatalf("inbox/One.md = %+v", one)
	}
	if _, ok := paths[".zennotes/comments/inbox/One.md.comments.json"]; !ok {
		t.Fatalf("synced .zennotes files are listed: %v", paths)
	}
	for p := range paths {
		if strings.HasPrefix(p, ".zennotes/sync") || strings.HasPrefix(p, "node_modules") || strings.HasSuffix(p, ".png") {
			t.Fatalf("%s must not be listed", p)
		}
	}
	gitignore, err := os.ReadFile(filepath.Join(v.Root(), ".zennotes", "sync", ".gitignore"))
	if err != nil || !strings.Contains(string(gitignore), "*") {
		t.Fatalf(".gitignore = %q, %v", gitignore, err)
	}

	again := reopenSyncVault(t, v, Options{})
	id, err := again.SyncVaultID()
	if err != nil || id != page.VaultID {
		t.Fatalf("vault_id after reopen = %q, %v; want %q", id, err, page.VaultID)
	}
	if got := syncItemAt(t, again, "inbox/One.md"); got.ItemID != one.ItemID || got.Revision != 1 {
		t.Fatalf("item after reopen = %+v", got)
	}
}

func TestSyncEveryWritePathRecordsChanges(t *testing.T) {
	v := newSyncVault(t)
	if _, err := v.WriteNote("inbox/Target.md", "# Target\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.WriteNote("inbox/Linker.md", "See [[Target]].\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.WriteNoteComments("inbox/Target.md", []NoteComment{{Body: "hi", AnchorText: "Target", AnchorStart: 2, AnchorEnd: 8}}); err != nil {
		t.Fatal(err)
	}
	target := syncItemAt(t, v, "inbox/Target.md")
	linker := syncItemAt(t, v, "inbox/Linker.md")
	comments := syncItemAt(t, v, ".zennotes/comments/inbox/Target.md.comments.json")
	cursor := v.sync.seq

	// A rename: one move for the note, one for its comments, and an upsert of
	// the note whose link was rewritten.
	if _, err := v.RenameNote("inbox/Target.md", "Renamed"); err != nil {
		t.Fatal(err)
	}
	changes := allChanges(t, v, cursor)
	byItem := map[string][]SyncChange{}
	for _, change := range changes {
		byItem[change.ItemID] = append(byItem[change.ItemID], change)
	}
	if got := byItem[target.ItemID]; len(got) != 1 || got[0].Type != "move" || got[0].Path != "inbox/Renamed.md" ||
		got[0].PreviousPath == nil || *got[0].PreviousPath != "inbox/Target.md" || got[0].Revision != target.Revision+1 {
		t.Fatalf("rename = %+v", got)
	}
	if got := byItem[comments.ItemID]; len(got) != 1 || got[0].Type != "move" || got[0].Path != ".zennotes/comments/inbox/Renamed.md.comments.json" {
		t.Fatalf("comments follow the rename as a move: %+v", got)
	}
	if got := byItem[linker.ItemID]; len(got) != 1 || got[0].Type != "upsert" || got[0].Content == nil || !strings.Contains(got[0].Content.Data, "[[Renamed]]") {
		t.Fatalf("the wikilink rewrite is an upsert of the linking note: %+v", got)
	}
	for i, change := range changes {
		if change.Sequence != cursor+int64(i)+1 {
			t.Fatalf("sequences are contiguous: %+v", changes)
		}
	}

	// Trash is a move into the trash folder; Empty Trash is a delete.
	cursor = v.sync.seq
	trashed, err := v.MoveToTrash("inbox/Renamed.md")
	if err != nil {
		t.Fatal(err)
	}
	changes = allChanges(t, v, cursor)
	trashMoves := map[string]SyncChange{}
	for _, change := range changes {
		trashMoves[change.ItemID] = change
	}
	if len(changes) != 2 || trashMoves[target.ItemID].Type != "move" || trashMoves[target.ItemID].Path != trashed.Path ||
		trashMoves[comments.ItemID].Type != "move" {
		t.Fatalf("trash = %+v", changes)
	}
	cursor = v.sync.seq
	if err := v.EmptyTrash(); err != nil {
		t.Fatal(err)
	}
	changes = allChanges(t, v, cursor)
	deleted := map[string]bool{}
	for _, change := range changes {
		if change.Type != "delete" {
			t.Fatalf("empty trash is deletes only: %+v", changes)
		}
		deleted[change.ItemID] = true
	}
	if !deleted[target.ItemID] || !deleted[comments.ItemID] {
		t.Fatalf("empty trash deletes the note and its comments: %+v", changes)
	}
	if _, ok := v.sync.tombstones[target.ItemID]; !ok {
		t.Fatal("a delete leaves a tombstone")
	}

	// A permanent delete, a duplicate, a create.
	cursor = v.sync.seq
	if err := v.DeleteNote("inbox/Linker.md"); err != nil {
		t.Fatal(err)
	}
	created, err := v.CreateNote(FolderInbox, "Fresh", "")
	if err != nil {
		t.Fatal(err)
	}
	dup, err := v.DuplicateNote(created.Path)
	if err != nil {
		t.Fatal(err)
	}
	changes = allChanges(t, v, cursor)
	if len(changes) != 3 || changes[0].Type != "delete" || changes[0].ItemID != linker.ItemID ||
		changes[1].Type != "upsert" || changes[1].Path != created.Path || changes[1].Revision != 1 ||
		changes[2].Type != "upsert" || changes[2].Path != dup.Path {
		t.Fatalf("delete, create, duplicate = %+v", changes)
	}

	// vault.json through the settings route.
	cursor = v.sync.seq
	settings, err := v.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	settings.Favorites = []string{created.Path}
	if _, err := v.SetSettings(settings); err != nil {
		t.Fatal(err)
	}
	changes = allChanges(t, v, cursor)
	if len(changes) != 1 || changes[0].Path != ".zennotes/vault.json" {
		t.Fatalf("settings write = %+v", changes)
	}
}

func TestSyncFolderRenameIsOneMovePerItem(t *testing.T) {
	v := newSyncVault(t)
	// Identical bytes, so only the rename's own hint can pair them right.
	for _, name := range []string{"A", "B", "C"} {
		writeRaw(t, v, "inbox/Projects/"+name+".md", "")
	}
	if err := v.SyncReconcile(); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, name := range []string{"A", "B", "C"} {
		ids[name] = syncItemAt(t, v, "inbox/Projects/"+name+".md").ItemID
	}
	cursor := v.sync.seq
	if _, err := v.RenameFolder(FolderInbox, "Projects", "Work"); err != nil {
		t.Fatal(err)
	}
	moves := 0
	for _, change := range allChanges(t, v, cursor) {
		if change.Type == "move" {
			moves++
			name := strings.TrimSuffix(filepath.Base(change.Path), ".md")
			if change.ItemID != ids[name] || change.Path != "inbox/Work/"+name+".md" {
				t.Fatalf("each note keeps its own id: %+v", change)
			}
		}
	}
	if moves != 3 {
		t.Fatalf("moves = %d", moves)
	}
}

func TestSyncFollowsRemappedSystemFolders(t *testing.T) {
	v := newSyncVault(t)
	settings, err := v.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	settings.SystemFolderPaths = map[string]string{"trash": "Deleted Notes"}
	if _, err := v.SetSettings(settings); err != nil {
		t.Fatal(err)
	}
	if _, err := v.WriteNote("inbox/Note.md", "x"); err != nil {
		t.Fatal(err)
	}
	cursor := v.sync.seq
	trashed, err := v.MoveToTrash("inbox/Note.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(trashed.Path, "Deleted Notes/") {
		t.Fatalf("trash path = %s", trashed.Path)
	}
	changes := allChanges(t, v, cursor)
	if len(changes) != 1 || changes[0].Type != "move" || changes[0].Path != trashed.Path {
		t.Fatalf("changes = %+v", changes)
	}
	// A sync delete of the last note in the trash keeps the trash folder.
	item := syncItemAt(t, v, trashed.Path)
	result, err := v.SyncMutate([]SyncMutation{{OperationID: "op-1", ItemID: item.ItemID, BaseRevision: rev(item.Revision), Type: "delete"}})
	if err != nil || len(result.Acknowledged) != 1 {
		t.Fatalf("delete = %+v, %v", result, err)
	}
	if info, err := os.Stat(filepath.Join(v.Root(), "Deleted Notes")); err != nil || !info.IsDir() {
		t.Fatalf("the remapped trash folder stays: %v", err)
	}
}

func TestSyncMutationsCheckTheBaseUnderTheLock(t *testing.T) {
	v := newSyncVault(t)
	result, err := v.SyncMutate([]SyncMutation{
		{OperationID: "create-1", ItemID: "item-1", Type: "upsert", Path: "inbox/New.md", Kind: "text", Content: syncText("one")},
	})
	if err != nil || len(result.Acknowledged) != 1 || result.Acknowledged[0].Revision != 1 || result.Cursor != result.Acknowledged[0].Sequence {
		t.Fatalf("create = %+v, %v", result, err)
	}
	if data, _ := os.ReadFile(filepath.Join(v.Root(), "inbox", "New.md")); string(data) != "one" {
		t.Fatalf("file = %q", data)
	}

	result, err = v.SyncMutate([]SyncMutation{
		{OperationID: "edit-1", ItemID: "item-1", BaseRevision: rev(1), Type: "upsert", Path: "inbox/New.md", Kind: "text", Content: syncText("two")},
		{OperationID: "stale-1", ItemID: "item-1", BaseRevision: rev(1), Type: "upsert", Path: "inbox/New.md", Kind: "text", Content: syncText("stale")},
		{OperationID: "again-1", ItemID: "item-1", Type: "upsert", Path: "inbox/New.md", Kind: "text", Content: syncText("dup")},
		{OperationID: "taken-1", ItemID: "item-2", Type: "upsert", Path: "inbox/new.md", Kind: "text", Content: syncText("case")},
		{OperationID: "gone-1", ItemID: "never", BaseRevision: rev(3), Type: "delete"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Acknowledged) != 1 || result.Acknowledged[0].OperationID != "edit-1" || result.Acknowledged[0].Revision != 2 {
		t.Fatalf("acks = %+v", result.Acknowledged)
	}
	codes := map[string]SyncConflict{}
	for _, conflict := range result.Conflicts {
		codes[conflict.OperationID] = conflict
	}
	if c := codes["stale-1"]; c.Code != "REVISION_CONFLICT" || c.CurrentRevision == nil || *c.CurrentRevision != 2 || c.CurrentPath == nil || *c.CurrentPath != "inbox/New.md" {
		t.Fatalf("stale base = %+v", c)
	}
	if c := codes["again-1"]; c.Code != "REVISION_CONFLICT" {
		t.Fatalf("creating a live item again = %+v", c)
	}
	if c := codes["taken-1"]; c.Code != "PATH_CONFLICT" || c.CurrentRevision != nil {
		t.Fatalf("a name another item holds, by case = %+v", c)
	}
	if c := codes["gone-1"]; c.Code != "ITEM_DELETED" {
		t.Fatalf("unknown item = %+v", c)
	}
	if data, _ := os.ReadFile(filepath.Join(v.Root(), "inbox", "New.md")); string(data) != "two" {
		t.Fatalf("only the current base lands: %q", data)
	}

	// A repeated operation id answers what it answered the first time, a
	// conflict included, without doing anything again.
	before := v.sync.seq
	replay, err := v.SyncMutate([]SyncMutation{
		{OperationID: "edit-1", ItemID: "item-1", BaseRevision: rev(1), Type: "upsert", Path: "inbox/New.md", Kind: "text", Content: syncText("two")},
		{OperationID: "stale-1", ItemID: "item-1", BaseRevision: rev(1), Type: "upsert", Path: "inbox/New.md", Kind: "text", Content: syncText("stale")},
	})
	if err != nil || len(replay.Acknowledged) != 1 || replay.Acknowledged[0] != result.Acknowledged[0] ||
		len(replay.Conflicts) != 1 || replay.Conflicts[0].Code != "REVISION_CONFLICT" || v.sync.seq != before {
		t.Fatalf("replay = %+v, %v", replay, err)
	}

	// Move, then delete; the deleted item stays deleted.
	result, err = v.SyncMutate([]SyncMutation{
		{OperationID: "move-1", ItemID: "item-1", BaseRevision: rev(2), Type: "move", Path: "inbox/Sub/Moved.md"},
	})
	if err != nil || len(result.Acknowledged) != 1 {
		t.Fatalf("move = %+v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(v.Root(), "inbox", "Sub", "Moved.md")); err != nil {
		t.Fatal(err)
	}
	result, err = v.SyncMutate([]SyncMutation{
		{OperationID: "delete-1", ItemID: "item-1", BaseRevision: rev(3), Type: "delete"},
		{OperationID: "delete-2", ItemID: "item-1", BaseRevision: rev(4), Type: "delete"},
		{OperationID: "revive-1", ItemID: "item-1", Type: "upsert", Path: "inbox/Back.md", Kind: "text", Content: syncText("x")},
	})
	if err != nil || len(result.Acknowledged) != 1 || len(result.Conflicts) != 2 ||
		result.Conflicts[0].Code != "ITEM_DELETED" || result.Conflicts[1].Code != "ITEM_DELETED" {
		t.Fatalf("delete = %+v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(v.Root(), "inbox", "Sub")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the folder the delete emptied goes too: %v", err)
	}
	if _, err := os.Stat(filepath.Join(v.Root(), "inbox")); err != nil {
		t.Fatalf("the inbox stays: %v", err)
	}

	// The replay table survives a restart.
	again := reopenSyncVault(t, v, Options{})
	replay, err = again.SyncMutate([]SyncMutation{{OperationID: "delete-2", ItemID: "item-1", BaseRevision: rev(4), Type: "delete"}})
	if err != nil || len(replay.Conflicts) != 1 || replay.Conflicts[0].Code != "ITEM_DELETED" {
		t.Fatalf("replay after restart = %+v, %v", replay, err)
	}
}

func TestSyncMutationRefusals(t *testing.T) {
	v := newSyncVault(t)
	writeRaw(t, v, "inbox/Latin1.md", "caf\xe9")
	cases := []struct {
		name     string
		mutation SyncMutation
		field    string
	}{
		{"binary", SyncMutation{OperationID: "b", ItemID: "b", Type: "upsert", Path: "assets/a.png", Kind: "binary",
			Content: &SyncContent{Encoding: "base64", Data: "AA==", Sha256: contentSha256([]byte{0}), ByteLength: 1, MediaType: "image/png"}}, "mutations.0.path"},
		{"not text", SyncMutation{OperationID: "t", ItemID: "t", Type: "upsert", Path: "assets/a.bin", Kind: "text", Content: syncText("x")}, "mutations.0.path"},
		{"local only", SyncMutation{OperationID: "l", ItemID: "l", Type: "upsert", Path: ".zennotes/sync/index.json", Kind: "text", Content: syncText("{}")}, "mutations.0.path"},
		{"bad name", SyncMutation{OperationID: "n", ItemID: "n", Type: "upsert", Path: "inbox/a:b.md", Kind: "text", Content: syncText("x")}, "mutations.0.path"},
		{"escape", SyncMutation{OperationID: "e", ItemID: "e", Type: "upsert", Path: "../out.md", Kind: "text", Content: syncText("x")}, "mutations.0.path"},
		{"wrong hash", SyncMutation{OperationID: "h", ItemID: "h", Type: "upsert", Path: "inbox/a.md", Kind: "text",
			Content: &SyncContent{Encoding: "utf8", Data: "x", Sha256: strings.Repeat("0", 64), ByteLength: 1, MediaType: "text/markdown"}}, "mutations.0.content"},
		{"move without base", SyncMutation{OperationID: "m", ItemID: "m", Type: "move", Path: "inbox/a.md"}, "mutations.0.base_revision"},
	}
	for _, tc := range cases {
		_, err := v.SyncMutate([]SyncMutation{tc.mutation})
		var problems *SyncValidationError
		if !errors.As(err, &problems) || len(problems.Fields) != 1 || problems.Fields[tc.field] == nil {
			t.Fatalf("%s: err = %v", tc.name, err)
		}
	}
	// A file sync does not carry is never written over by a new one.
	_, err := v.SyncMutate([]SyncMutation{{OperationID: "over", ItemID: "over", Type: "upsert", Path: "inbox/Latin1.md", Kind: "text", Content: syncText("new")}})
	var problems *SyncValidationError
	if !errors.As(err, &problems) || problems.Fields["mutations.0.path"] == nil {
		t.Fatalf("over an unsynced file: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(v.Root(), "inbox", "Latin1.md")); string(data) != "caf\xe9" {
		t.Fatalf("the unsynced file is untouched: %q", data)
	}
}

func TestSyncOutOfBandEditsAreNeverWrittenOver(t *testing.T) {
	v := newSyncVault(t)
	if _, err := v.WriteNote("inbox/Note.md", "server"); err != nil {
		t.Fatal(err)
	}
	item := syncItemAt(t, v, "inbox/Note.md")
	// An edit from a shell, before any reconcile ran. Same size, and the
	// index's mtime is racy, so only the live hash can tell.
	writeRaw(t, v, "inbox/Note.md", "SHELL!")
	result, err := v.SyncMutate([]SyncMutation{{
		OperationID: "op", ItemID: item.ItemID, BaseRevision: rev(item.Revision), Type: "upsert",
		Path: "inbox/Note.md", Kind: "text", Content: syncText("client"),
	}})
	if err != nil || len(result.Conflicts) != 1 || result.Conflicts[0].Code != "REVISION_CONFLICT" ||
		*result.Conflicts[0].CurrentRevision != item.Revision+1 {
		t.Fatalf("result = %+v, %v", result, err)
	}
	if data, _ := os.ReadFile(filepath.Join(v.Root(), "inbox", "Note.md")); string(data) != "SHELL!" {
		t.Fatalf("the shell edit stays: %q", data)
	}
	changes := allChanges(t, v, 0)
	last := changes[len(changes)-1]
	if last.Type != "upsert" || last.Content == nil || last.Content.Data != "SHELL!" || last.Sequence > result.Cursor {
		t.Fatalf("the shell edit is in the log: %+v", last)
	}
}

func TestSyncReconcileFindsMovesAndEdits(t *testing.T) {
	v := newSyncVault(t)
	writeRaw(t, v, "inbox/Keep.md", "keep")
	writeRaw(t, v, "inbox/Move.md", "unique bytes")
	writeRaw(t, v, "inbox/Edit.md", "before")
	writeRaw(t, v, "inbox/Gone.md", "gone")
	if err := v.SyncReconcile(); err != nil {
		t.Fatal(err)
	}
	moved, gone, edit := syncItemAt(t, v, "inbox/Move.md"), syncItemAt(t, v, "inbox/Gone.md"), syncItemAt(t, v, "inbox/Edit.md")
	cursor := v.sync.seq

	if err := os.Rename(filepath.Join(v.Root(), "inbox", "Move.md"), filepath.Join(v.Root(), "inbox", "Elsewhere.md")); err != nil {
		t.Fatal(err)
	}
	writeRaw(t, v, "inbox/Edit.md", "after, and longer")
	if err := os.Remove(filepath.Join(v.Root(), "inbox", "Gone.md")); err != nil {
		t.Fatal(err)
	}
	writeRaw(t, v, "Loose.md", "new at the root")
	if err := v.SyncReconcile(); err != nil {
		t.Fatal(err)
	}
	got := map[string]SyncChange{}
	for _, change := range allChanges(t, v, cursor) {
		got[change.Type+":"+change.Path] = change
	}
	if c, ok := got["move:inbox/Elsewhere.md"]; !ok || c.ItemID != moved.ItemID {
		t.Fatalf("a rename by hand is a move: %+v", got)
	}
	if c, ok := got["delete:inbox/Gone.md"]; !ok || c.ItemID != gone.ItemID {
		t.Fatalf("a removal is a delete: %+v", got)
	}
	if c, ok := got["upsert:inbox/Edit.md"]; !ok || c.ItemID != edit.ItemID || c.Revision != edit.Revision+1 {
		t.Fatalf("an edit is an upsert: %+v", got)
	}
	if c, ok := got["upsert:Loose.md"]; !ok || c.Revision != 1 {
		t.Fatalf("a new file is a new item: %+v", got)
	}
	if len(got) != 4 {
		t.Fatalf("Keep.md is untouched: %+v", got)
	}
	// A second pass finds nothing.
	cursor = v.sync.seq
	if err := v.SyncReconcile(); err != nil || v.sync.seq != cursor {
		t.Fatalf("second reconcile moved the cursor: %v", err)
	}
}

func TestSyncCrashBetweenWriteAndLogYieldsAnExtraRevision(t *testing.T) {
	v := newSyncVault(t)
	if _, err := v.WriteNote("inbox/Note.md", "one"); err != nil {
		t.Fatal(err)
	}
	item := syncItemAt(t, v, "inbox/Note.md")
	// The atomic write landed, the log append did not (the server died).
	writeRaw(t, v, "inbox/Note.md", "two, written before the crash")
	// And a torn half line at the end of the log.
	logFile, err := os.OpenFile(filepath.Join(v.Root(), ".zennotes", "sync", "log.jsonl"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = logFile.WriteString(`{"k":"c","seq":99,"item_`)
	_ = logFile.Close()

	again := reopenSyncVault(t, v, Options{})
	if err := again.SyncReconcile(); err != nil {
		t.Fatal(err)
	}
	got := syncItemAt(t, again, "inbox/Note.md")
	if got.ItemID != item.ItemID || got.Revision != item.Revision+1 || got.Sha256 != contentSha256([]byte("two, written before the crash")) {
		t.Fatalf("after restart = %+v", got)
	}
	if _, err := again.SyncChanges(0, 100); err != nil {
		t.Fatalf("the torn line is dropped, the log reads: %v", err)
	}
}

func TestSyncCursorExpiresWithTheLog(t *testing.T) {
	v := newSyncVault(t)
	if _, err := v.WriteNote("inbox/A.md", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.WriteNote("inbox/B.md", "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.WriteNote("inbox/A.md", "a, rewritten"); err != nil {
		t.Fatal(err)
	}
	old := v.sync.seq
	v = reopenSyncVault(t, v, Options{SyncLogRetention: time.Millisecond})
	time.Sleep(5 * time.Millisecond)
	if err := v.SyncPrune(); err != nil {
		t.Fatal(err)
	}
	if _, err := v.SyncChanges(0, 100); !errors.Is(err, ErrSyncCursorExpired) {
		t.Fatalf("a cursor before the floor = %v", err)
	}
	if page, err := v.SyncChanges(old, 100); err != nil || len(page.Data) != 0 {
		t.Fatalf("the newest cursor still reads: %+v, %v", page, err)
	}
	if _, err := v.SyncChanges(old+5, 100); !errors.Is(err, ErrSyncCursorExpired) {
		t.Fatalf("a cursor past the head = %v", err)
	}
	// The manifest still has everything. The blobs of superseded bytes went
	// with the log; those of every item's current bytes stay, for revision
	// reads.
	page, err := v.SyncManifest(1, 250, true)
	if err != nil || page.Cursor != old || len(page.Data) < 2 {
		t.Fatalf("manifest = %+v, %v", page, err)
	}
	blobs := map[string]bool{}
	entries, _ := os.ReadDir(filepath.Join(v.Root(), ".zennotes", "sync", "blobs"))
	for _, dir := range entries {
		files, _ := os.ReadDir(filepath.Join(v.Root(), ".zennotes", "sync", "blobs", dir.Name()))
		for _, file := range files {
			blobs[strings.TrimSuffix(file.Name(), ".gz")] = true
		}
	}
	if blobs[contentSha256([]byte("a"))] || !blobs[contentSha256([]byte("a, rewritten"))] || !blobs[contentSha256([]byte("b"))] {
		t.Fatalf("blobs after the prune = %v", blobs)
	}
	// A restart after the prune keeps the floor.
	again := reopenSyncVault(t, v, Options{})
	if _, err := again.SyncChanges(0, 100); !errors.Is(err, ErrSyncCursorExpired) {
		t.Fatalf("after restart = %v", err)
	}
}

func TestSyncPruneInterruptedBeforeTheLogRewrite(t *testing.T) {
	v := newSyncVault(t)
	if _, err := v.WriteNote("inbox/A.md", "a"); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(v.Root(), ".zennotes", "sync", "log.jsonl")
	before, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	id := v.sync.vaultID
	v = reopenSyncVault(t, v, Options{SyncLogRetention: time.Millisecond})
	time.Sleep(5 * time.Millisecond)
	if err := v.SyncPrune(); err != nil {
		t.Fatal(err)
	}
	// Put the old log back, as if the rewrite never happened.
	if err := os.WriteFile(logPath, before, 0o600); err != nil {
		t.Fatal(err)
	}
	again := reopenSyncVault(t, v, Options{})
	if got, _ := again.SyncVaultID(); got != id {
		t.Fatalf("the store was reset: vault_id %s, was %s", got, id)
	}
	if _, err := again.WriteNote("inbox/B.md", "b"); err != nil {
		t.Fatal(err)
	}
	if page, err := again.SyncChanges(again.sync.floor, 10); err != nil || len(page.Data) != 1 {
		t.Fatalf("changes after the floor = %+v, %v", page, err)
	}
}

func TestSyncAnUnreadableFileNeverStopsSync(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs POSIX permissions and a non-root user")
	}
	v := newSyncVault(t)
	writeRaw(t, v, "inbox/Locked.md", "secret")
	writeRaw(t, v, "inbox/Shut/Inside.md", "inside")
	if err := v.SyncReconcile(); err != nil {
		t.Fatal(err)
	}
	locked, inside := syncItemAt(t, v, "inbox/Locked.md"), syncItemAt(t, v, "inbox/Shut/Inside.md")
	lockedAbs := filepath.Join(v.Root(), "inbox", "Locked.md")
	shut := filepath.Join(v.Root(), "inbox", "Shut")
	writeRaw(t, v, "inbox/Locked.md", "secret, changed")
	if err := os.Chmod(lockedAbs, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shut, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockedAbs, 0o600); _ = os.Chmod(shut, 0o700) })
	writeRaw(t, v, "inbox/Other.md", "other")

	cursor := v.sync.seq
	if _, err := v.SyncManifest(1, 250, false); err != nil {
		t.Fatalf("one unreadable file and folder must not stop the manifest: %v", err)
	}
	for _, change := range allChanges(t, v, cursor) {
		if change.ItemID == locked.ItemID || change.ItemID == inside.ItemID {
			t.Fatalf("an unreadable file is neither changed nor deleted: %+v", change)
		}
	}
	if got := syncItemAt(t, v, "inbox/Other.md"); got.Revision != 1 {
		t.Fatalf("the rest syncs: %+v", got)
	}
}

func TestSyncCaseOnlyRenameIsRecorded(t *testing.T) {
	v := newSyncVault(t)
	if _, err := v.WriteNote("inbox/foo.md", "x"); err != nil {
		t.Fatal(err)
	}
	item := syncItemAt(t, v, "inbox/foo.md")
	cursor := v.sync.seq
	meta, err := v.RenameNote("inbox/foo.md", "Foo")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Path != "inbox/Foo.md" {
		t.Fatalf("rename = %s", meta.Path)
	}
	changes := allChanges(t, v, cursor)
	if len(changes) != 1 || changes[0].Type != "move" || changes[0].ItemID != item.ItemID || changes[0].Path != "inbox/Foo.md" {
		t.Fatalf("a case-only rename is one move: %+v", changes)
	}
}

func TestSyncManifestPages(t *testing.T) {
	v := newSyncVault(t)
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		writeRaw(t, v, "inbox/"+name+".md", name)
	}
	first, err := v.SyncManifest(1, 2, false)
	if err != nil || len(first.Data) != 2 || first.NextPage == nil || *first.NextPage != 2 {
		t.Fatalf("page 1 = %+v, %v", first, err)
	}
	var all []string
	for page := 1; ; page++ {
		out, err := v.SyncManifest(page, 2, false)
		if err != nil || out.Cursor != first.Cursor {
			t.Fatalf("page %d = %+v, %v", page, out, err)
		}
		for _, item := range out.Data {
			all = append(all, item.Path)
			if item.Content == nil {
				t.Fatalf("small text rides inline: %+v", item)
			}
		}
		if out.NextPage == nil {
			break
		}
	}
	if strings.Join(all, ",") != "inbox/Welcome.md,inbox/a.md,inbox/b.md,inbox/c.md,inbox/d.md,inbox/e.md" {
		t.Fatalf("pages = %v", all)
	}
}

func TestSyncCaseCollisionsListOneFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "probe"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "PROBE")); err == nil || runtime.GOOS == "windows" {
		t.Skip("needs a case-sensitive disk")
	}
	v := newSyncVault(t)
	writeRaw(t, v, "inbox/Note.md", "one")
	writeRaw(t, v, "inbox/note.md", "two")
	page, err := v.SyncManifest(1, 250, false)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, item := range page.Data {
		if strings.EqualFold(item.Path, "inbox/note.md") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("two names one case apart list once, got %d", count)
	}
}

func TestSyncPathRulesMatchTheEngine(t *testing.T) {
	cases := map[string]bool{
		"inbox/Note.md":        true,
		".zennotes/vault.json": true,
		".zennotes/comments/inbox/a.md.comments.json": true,
		".zennotes/templates/x.md":                    true,
		".zennotes/workflows/w.md":                    true,
		".zennotes/agent-edits/x.json":                true,
		".zennotes/note-meta-cache-v1.json":           false,
		".zennotes/sync/log.jsonl":                    false,
		".git/config":                                 false,
		"a/node_modules/b.md":                         false,
		"x.tmp":                                       false,
		".DS_Store":                                   false,
		"a//b.md":                                     false,
		"a:b.md":                                      false,
	}
	for p, want := range cases {
		if got := shouldSyncPath(p); got != want {
			t.Errorf("shouldSyncPath(%q) = %v, want %v", p, got, want)
		}
	}
	if got, _ := normalizeSyncPath("Café.md"); got != "Café.md" {
		t.Errorf("NFC: %q", got)
	}
}

// Revision reads (supportsSync 2): what the engine fetches as a conflict's
// base. Every revision the log or the index can still describe answers, a
// file the store started with included, after the file has changed.
func TestSyncRevisionReads(t *testing.T) {
	v, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	writeRaw(t, v, "inbox/Base.md", "# Base\n\none\ntwo\n")
	if _, err := v.SyncVaultID(); err != nil {
		t.Fatal(err)
	}
	item := syncItemAt(t, v, "inbox/Base.md")
	if item.Revision != 1 {
		t.Fatalf("a file the store starts with is revision 1: %+v", item)
	}
	want := func(revision int64, path, body string) {
		t.Helper()
		got, err := v.SyncReadRevision(item.ItemID, revision)
		if err != nil {
			t.Fatalf("revision %d: %v", revision, err)
		}
		if got.ItemID != item.ItemID || got.Revision != revision || got.Path != path || got.Kind != "text" || got.Deleted ||
			got.Content == nil || got.Content.Encoding != "utf8" || got.Content.Data != body ||
			got.Content.Sha256 != contentSha256([]byte(body)) || got.Content.ByteLength != int64(len(body)) ||
			got.Content.MediaType != "text/markdown" {
			t.Fatalf("revision %d = %+v (content %+v)", revision, got, got.Content)
		}
	}
	want(1, "inbox/Base.md", "# Base\n\none\ntwo\n")

	// An edit in a shell: revision 2. Revision 1 is gone from the disk and
	// still answers, from the blob the store kept when it started.
	writeRaw(t, v, "inbox/Base.md", "# Base\n\none (edited)\ntwo\n")
	if err := v.SyncReconcile(); err != nil {
		t.Fatal(err)
	}
	want(1, "inbox/Base.md", "# Base\n\none\ntwo\n")
	want(2, "inbox/Base.md", "# Base\n\none (edited)\ntwo\n")

	// A move (revision 3) answers its new path with the bytes it carried.
	result, err := v.SyncMutate([]SyncMutation{{OperationID: "mv", ItemID: item.ItemID, BaseRevision: rev(2), Type: "move", Path: "inbox/Moved.md"}})
	if err != nil || len(result.Acknowledged) != 1 {
		t.Fatalf("move = %+v, %v", result, err)
	}
	want(3, "inbox/Moved.md", "# Base\n\none (edited)\ntwo\n")
	want(2, "inbox/Base.md", "# Base\n\none (edited)\ntwo\n")

	// A delete (revision 4) answers deleted, with no content.
	result, err = v.SyncMutate([]SyncMutation{{OperationID: "rm", ItemID: item.ItemID, BaseRevision: rev(3), Type: "delete"}})
	if err != nil || len(result.Acknowledged) != 1 {
		t.Fatalf("delete = %+v, %v", result, err)
	}
	got, err := v.SyncReadRevision(item.ItemID, 4)
	if err != nil || !got.Deleted || got.Content != nil || got.Path != "inbox/Moved.md" || got.Revision != 4 {
		t.Fatalf("the delete = %+v, %v", got, err)
	}
	want(3, "inbox/Moved.md", "# Base\n\none (edited)\ntwo\n")

	for _, c := range []struct {
		id       string
		revision int64
	}{{item.ItemID, 5}, {item.ItemID, 0}, {item.ItemID, -1}, {"no-such-item", 1}, {"", 1}} {
		if _, err := v.SyncReadRevision(c.id, c.revision); !errors.Is(err, ErrSyncRevisionNotRetained) {
			t.Fatalf("%q revision %d = %v", c.id, c.revision, err)
		}
	}

	// A move recorded before moves named their bytes still answers, from
	// the upsert before it.
	v.mu.Lock()
	for i := range v.sync.changes {
		if v.sync.changes[i].Type == "move" {
			v.sync.changes[i].Sha256, v.sync.changes[i].MediaType = "", ""
		}
	}
	v.mu.Unlock()
	want(3, "inbox/Moved.md", "# Base\n\none (edited)\ntwo\n")
}

// A revision whose bytes are gone (a store copied without its blobs, or a
// blob removed by hand) answers not retained, never wrong bytes, unless the
// file still holds exactly those bytes.
func TestSyncRevisionWithoutItsBlob(t *testing.T) {
	v := newSyncVault(t)
	if _, err := v.WriteNote("inbox/Note.md", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.WriteNote("inbox/Note.md", "second"); err != nil {
		t.Fatal(err)
	}
	item := syncItemAt(t, v, "inbox/Note.md")
	if err := os.RemoveAll(filepath.Join(v.Root(), ".zennotes", "sync", "blobs")); err != nil {
		t.Fatal(err)
	}
	if _, err := v.SyncReadRevision(item.ItemID, item.Revision-1); !errors.Is(err, ErrSyncRevisionNotRetained) {
		t.Fatalf("a revision with no bytes left = %v", err)
	}
	got, err := v.SyncReadRevision(item.ItemID, item.Revision)
	if err != nil || got.Content == nil || got.Content.Data != "second" {
		t.Fatalf("the current revision reads the file = %+v, %v", got, err)
	}
}

// A store from before current versions had blobs (server 2.91.1) fills them
// in at its first full reconcile, so revision 1 of a file it started with
// answers after that file changes.
func TestSyncRevisionBackfillsCurrentBlobs(t *testing.T) {
	v, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	writeRaw(t, v, "inbox/Old.md", "from before the upgrade")
	if _, err := v.SyncVaultID(); err != nil {
		t.Fatal(err)
	}
	item := syncItemAt(t, v, "inbox/Old.md")
	blobs := filepath.Join(v.Root(), ".zennotes", "sync", "blobs")
	if err := os.RemoveAll(blobs); err != nil {
		t.Fatal(err)
	}
	v = reopenSyncVault(t, v, Options{})
	if err := v.SyncReconcile(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(blobs, item.Sha256[:2], item.Sha256+".gz")); err != nil {
		t.Fatalf("the current version has a blob again: %v", err)
	}
	writeRaw(t, v, "inbox/Old.md", "edited after the upgrade")
	if err := v.SyncReconcile(); err != nil {
		t.Fatal(err)
	}
	got, err := v.SyncReadRevision(item.ItemID, 1)
	if err != nil || got.Content == nil || got.Content.Data != "from before the upgrade" {
		t.Fatalf("revision 1 = %+v, %v", got, err)
	}
}

// The retention window bounds revision reads as it bounds the change feed: a
// pruned revision answers not retained, the current one still answers.
func TestSyncRevisionAfterThePrune(t *testing.T) {
	v := newSyncVault(t)
	if _, err := v.WriteNote("inbox/Note.md", "old"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.WriteNote("inbox/Note.md", "current"); err != nil {
		t.Fatal(err)
	}
	item := syncItemAt(t, v, "inbox/Note.md")
	v = reopenSyncVault(t, v, Options{SyncLogRetention: time.Millisecond})
	time.Sleep(5 * time.Millisecond)
	if err := v.SyncPrune(); err != nil {
		t.Fatal(err)
	}
	if _, err := v.SyncReadRevision(item.ItemID, item.Revision-1); !errors.Is(err, ErrSyncRevisionNotRetained) {
		t.Fatalf("a pruned revision = %v", err)
	}
	// The file changes and the blob is what is left of the current bytes.
	writeRaw(t, v, "inbox/Note.md", "changed, not yet reconciled")
	got, err := v.SyncReadRevision(item.ItemID, item.Revision)
	if err != nil || got.Content == nil || got.Content.Data != "current" {
		t.Fatalf("the current revision after the prune = %+v, %v", got, err)
	}
}
