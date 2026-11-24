package vault

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// The sync protocol's three operations, in the wire shapes of ZenNotes Cloud's
// (packages/bridge-contract/src/cloud-sync.ts in ZenNotes/zennotes) minus
// encryption and content references, so Cloud's sync engine
// (CloudSyncCoordinator) can keep a local mirror of this vault.

const (
	// SyncMaxMutations is the largest batch a mutations request takes, the
	// engine's own batch size.
	SyncMaxMutations       = 100
	syncDefaultPerPage     = 250
	syncMaxPerPage         = 1000
	syncDefaultChangeLimit = 100
	syncMaxChangeLimit     = 1000
	// A changes page stops early once its content passes this, so one page
	// never carries the whole history of a large note.
	syncChangesPageBytes = 16 << 20
)

type SyncContent struct {
	Encoding   string `json:"encoding"`
	Data       string `json:"data"`
	Sha256     string `json:"sha256"`
	ByteLength int64  `json:"byte_length"`
	MediaType  string `json:"media_type"`
}

type SyncManifestItem struct {
	ItemID     string       `json:"item_id"`
	Path       string       `json:"path"`
	Kind       string       `json:"kind"`
	Revision   int64        `json:"revision"`
	Sha256     string       `json:"sha256"`
	ByteLength int64        `json:"byte_length"`
	MediaType  string       `json:"media_type"`
	Content    *SyncContent `json:"content,omitempty"`
}

type SyncManifestPage struct {
	VaultID  string             `json:"vault_id"`
	Data     []SyncManifestItem `json:"data"`
	Cursor   int64              `json:"cursor"`
	NextPage *int               `json:"next_page"`
}

type SyncChange struct {
	Sequence     int64        `json:"sequence"`
	ItemID       string       `json:"item_id"`
	Type         string       `json:"type"`
	Path         string       `json:"path"`
	PreviousPath *string      `json:"previous_path"`
	Revision     int64        `json:"revision"`
	Content      *SyncContent `json:"content,omitempty"`
}

type SyncChangesPage struct {
	VaultID string       `json:"vault_id"`
	Data    []SyncChange `json:"data"`
	Cursor  int64        `json:"cursor"`
	HasMore bool         `json:"has_more"`
}

type SyncMutation struct {
	OperationID  string       `json:"operation_id"`
	ItemID       string       `json:"item_id"`
	BaseRevision *int64       `json:"base_revision"`
	Type         string       `json:"type"`
	Path         string       `json:"path,omitempty"`
	Kind         string       `json:"kind,omitempty"`
	Content      *SyncContent `json:"content,omitempty"`
}

type SyncAck struct {
	OperationID string `json:"operation_id"`
	ItemID      string `json:"item_id"`
	Revision    int64  `json:"revision"`
	Sequence    int64  `json:"sequence"`
}

type SyncConflict struct {
	OperationID     string  `json:"operation_id"`
	ItemID          string  `json:"item_id"`
	Code            string  `json:"code"`
	CurrentRevision *int64  `json:"current_revision"`
	CurrentPath     *string `json:"current_path"`
}

type SyncMutationResult struct {
	VaultID      string         `json:"vault_id"`
	Acknowledged []SyncAck      `json:"acknowledged"`
	Conflicts    []SyncConflict `json:"conflicts"`
	Cursor       int64          `json:"cursor"`
}

// SyncValidationError refuses a mutations request, field by field, the way
// Cloud's 422 VALIDATION_FAILED does. When every field is a mutation's
// `path`, the engine sets those files aside and sends the rest again
// (cloud-sync-refusal.ts); anything else stops its run.
type SyncValidationError struct {
	Fields map[string][]string
}

func (e *SyncValidationError) Error() string {
	keys := make([]string, 0, len(e.Fields))
	for key := range e.Fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return "invalid sync request"
	}
	return fmt.Sprintf("%s: %s", keys[0], strings.Join(e.Fields[keys[0]], " "))
}

func (e *SyncValidationError) add(field, message string) {
	if e.Fields == nil {
		e.Fields = map[string][]string{}
	}
	e.Fields[field] = append(e.Fields[field], message)
}

// ErrSyncRetry answers a read that met a file changing under it.
var ErrSyncRetry = errors.New("the vault changed while it was being read; sync again")

const (
	syncRefusedKind    = "The server does not keep this kind of file in offline copies yet."
	syncRefusedName    = "This name is not valid on every system ZenNotes runs on."
	syncRefusedSize    = "This file is larger than the server takes."
	syncRefusedPlace   = "The server cannot put a file there."
	syncRefusedNotSync = "This file is not one that syncs."
)

// --- The store's lifecycle ---

// syncOpen loads the store a vault already has, at startup. A store that
// cannot be trusted starts over, with a new vault id.
func (v *Vault) syncOpen() {
	if !syncStoreExists(v.root) {
		return
	}
	s := newSyncStoreShell(v.root, v.fileMode, v.dirMode, v.maxSyncText, v.syncRetention)
	if err := s.load(); err != nil {
		if !errors.Is(err, errSyncDamaged) {
			// Left unloaded: the next sync request tries again.
			log.Printf("sync: the store in %s could not be read (%v); trying again on next use", s.dir, err)
			return
		}
		log.Printf("sync: the store in %s is damaged (%v); starting a new one, and offline copies will rebuild", s.dir, err)
		if err := s.initialize(v); err != nil {
			log.Printf("sync: %v", err)
			return
		}
	}
	v.sync = s
}

func (v *Vault) syncEnsureLocked() error {
	if v.sync != nil {
		return nil
	}
	s := newSyncStoreShell(v.root, v.fileMode, v.dirMode, v.maxSyncText, v.syncRetention)
	if syncStoreExists(v.root) {
		err := s.load()
		if err == nil {
			v.sync = s
			return nil
		}
		if !errors.Is(err, errSyncDamaged) {
			return fmt.Errorf("%w: %v", ErrSyncUnavailable, err)
		}
		log.Printf("sync: the store in %s is damaged (%v); starting a new one, and offline copies will rebuild", s.dir, err)
		s = newSyncStoreShell(v.root, v.fileMode, v.dirMode, v.maxSyncText, v.syncRetention)
	}
	if err := s.initialize(v); err != nil {
		return fmt.Errorf("%w: %v", ErrSyncUnavailable, err)
	}
	v.sync = s
	return nil
}

// syncFailedLocked runs when the store could not record a batch. What reached
// the disk is reloaded; anything that did not is in the files, where the next
// reconcile finds it.
func (v *Vault) syncFailedLocked(err error) {
	log.Printf("sync: could not record a change (%v); reloading the store", err)
	s := newSyncStoreShell(v.root, v.fileMode, v.dirMode, v.maxSyncText, v.syncRetention)
	if loadErr := s.load(); loadErr != nil {
		log.Printf("sync: the store could not be reloaded (%v); it starts over on next use", loadErr)
		v.sync = nil
		return
	}
	v.sync = s
}

// SyncActive reports whether this vault has a sync store.
func (v *Vault) SyncActive() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.sync != nil
}

// SyncVaultID is the vault's sync identity, starting the store if needed.
func (v *Vault) SyncVaultID() (string, error) {
	v.mu.RLock()
	if v.sync != nil {
		id := v.sync.vaultID
		v.mu.RUnlock()
		return id, nil
	}
	v.mu.RUnlock()
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.syncEnsureLocked(); err != nil {
		return "", err
	}
	return v.sync.vaultID, nil
}

// SyncReconcile records edits made outside the server, for a vault with a
// store. The maintenance loop runs it at startup, on a watcher hint and every
// few minutes, and prunes the log once a day.
func (v *Vault) SyncReconcile() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.sync == nil {
		return nil
	}
	if err := v.syncReconcileLocked(syncScope{all: true}); err != nil {
		v.syncFailedLocked(err)
		return err
	}
	if time.Since(v.sync.lastPrune) >= 24*time.Hour {
		if err := v.sync.prune(time.Now()); err != nil {
			v.syncFailedLocked(err)
			return err
		}
	}
	return nil
}

// SyncPrune drops the change log and blobs older than the retention window.
func (v *Vault) SyncPrune() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.sync == nil {
		return nil
	}
	if err := v.sync.prune(time.Now()); err != nil {
		v.syncFailedLocked(err)
		return err
	}
	return nil
}

func (v *Vault) syncReconcileBeforeRead(always bool) error {
	if !always {
		v.mu.RLock()
		fresh := v.sync != nil && time.Since(v.sync.lastReconcile) < syncMinReconcileInterval
		v.mu.RUnlock()
		if fresh {
			return nil
		}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.syncEnsureLocked(); err != nil {
		return err
	}
	if !always && time.Since(v.sync.lastReconcile) < syncMinReconcileInterval {
		return nil
	}
	if err := v.syncReconcileLocked(syncScope{all: true}); err != nil {
		v.syncFailedLocked(err)
		return fmt.Errorf("%w: %v", ErrSyncUnavailable, err)
	}
	return nil
}

// --- Reads ---

// syncCurrentBytes reads an item's bytes as the index describes them: the file
// when it still holds them, else the retained blob.
func (v *Vault) syncCurrentBytes(item *syncItem) ([]byte, error) {
	abs := filepath.Join(v.root, filepath.FromSlash(item.Path))
	if data, err := os.ReadFile(abs); err == nil && contentSha256(data) == item.Sha256 {
		return data, nil
	}
	if data, err := v.sync.readBlob(item.Sha256); err == nil {
		return data, nil
	}
	return nil, ErrSyncRetry
}

func syncTextContent(data []byte, sha, media string) *SyncContent {
	return &SyncContent{Encoding: "utf8", Data: string(data), Sha256: sha, ByteLength: int64(len(data)), MediaType: media}
}

// SyncManifest lists every synced item, sorted by path, one page at a time,
// each with its content inline. Until revision reads exist (phase 4) the
// engine has no other way to fetch an item's bytes, and one of its paths
// (settling a moved arrival) asks for the manifest without content and
// still needs them. includeContent is accepted for Cloud's spelling of the
// request and changes nothing.
// The first page reconciles first, so the list starts from the disk as it is.
// Every page carries the cursor; a client that sees it move between pages
// starts over, which is how it gets one consistent list.
func (v *Vault) SyncManifest(page, perPage int, includeContent bool) (SyncManifestPage, error) {
	_ = includeContent
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = syncDefaultPerPage
	}
	perPage = min(perPage, syncMaxPerPage)
	// Past any vault's last page, and small enough that page*perPage cannot
	// overflow.
	page = min(page, 1<<30/syncMaxPerPage)
	if err := v.syncReconcileBeforeRead(page == 1); err != nil {
		return SyncManifestPage{}, err
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	s := v.sync
	if s == nil {
		return SyncManifestPage{}, ErrSyncUnavailable
	}
	items := make([]*syncItem, 0, len(s.items))
	for _, item := range s.items {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	out := SyncManifestPage{VaultID: s.vaultID, Data: []SyncManifestItem{}, Cursor: s.seq}
	start := (page - 1) * perPage
	end := min(start+perPage, len(items))
	for i := start; i < end; i++ {
		item := items[i]
		entry := SyncManifestItem{
			ItemID: item.ItemID, Path: item.Path, Kind: "text", Revision: item.Revision,
			Sha256: item.Sha256, ByteLength: item.ByteLength, MediaType: item.MediaType,
		}
		data, err := v.syncCurrentBytes(item)
		if err != nil {
			return SyncManifestPage{}, err
		}
		entry.Content = syncTextContent(data, item.Sha256, item.MediaType)
		out.Data = append(out.Data, entry)
	}
	if end < len(items) {
		next := page + 1
		out.NextPage = &next
	}
	return out, nil
}

// SyncChanges returns the change log after a cursor. A cursor older than the
// retained log (or newer than this log ever was, as after a restored backup)
// answers ErrSyncCursorExpired, and the client rebuilds from the manifest.
func (v *Vault) SyncChanges(after int64, limit int) (SyncChangesPage, error) {
	if limit < 1 {
		limit = syncDefaultChangeLimit
	}
	limit = min(limit, syncMaxChangeLimit)
	if err := v.syncReconcileBeforeRead(false); err != nil {
		return SyncChangesPage{}, err
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	s := v.sync
	if s == nil {
		return SyncChangesPage{}, ErrSyncUnavailable
	}
	if after < s.floor || after > s.seq {
		return SyncChangesPage{}, ErrSyncCursorExpired
	}
	out := SyncChangesPage{VaultID: s.vaultID, Data: []SyncChange{}, Cursor: after}
	budget := 0
	i := int(after - s.floor)
	for ; i < len(s.changes) && len(out.Data) < limit; i++ {
		rec := s.changes[i]
		change := SyncChange{Sequence: rec.Seq, ItemID: rec.ItemID, Type: rec.Type, Path: rec.Path, Revision: rec.Revision}
		if rec.PreviousPath != "" {
			previous := rec.PreviousPath
			change.PreviousPath = &previous
		}
		if rec.Type == "upsert" {
			data, err := s.readBlob(rec.Sha256)
			if err != nil {
				// No retained bytes for this revision (a store copied without
				// its blobs). The file may still hold them; if not, the
				// client catches up from the manifest instead.
				item := s.items[rec.ItemID]
				if item == nil || item.Sha256 != rec.Sha256 {
					return SyncChangesPage{}, ErrSyncCursorExpired
				}
				if data, err = v.syncCurrentBytes(item); err != nil {
					return SyncChangesPage{}, ErrSyncCursorExpired
				}
			}
			if len(out.Data) > 0 && budget+len(data) > syncChangesPageBytes {
				break
			}
			budget += len(data)
			change.Content = syncTextContent(data, rec.Sha256, rec.MediaType)
		}
		out.Data = append(out.Data, change)
		out.Cursor = rec.Seq
	}
	out.HasMore = i < len(s.changes)
	return out, nil
}

// --- Mutations ---

// syncRefusal is a path refusal found while a batch was being applied, after
// some of it may already have landed. The answer is the same 422 a refusal
// up front gets; what landed is answered again, from the replay table, when
// the engine sends the rest of the batch.
type syncRefusal struct {
	index   int
	message string
}

func (e syncRefusal) Error() string { return e.message }

func validateSyncMutations(v *Vault, mutations []SyncMutation) *SyncValidationError {
	problems := &SyncValidationError{}
	if len(mutations) > SyncMaxMutations {
		problems.add("mutations", fmt.Sprintf("A request takes at most %d changes.", SyncMaxMutations))
		return problems
	}
	for i, m := range mutations {
		field := func(name string) string { return fmt.Sprintf("mutations.%d.%s", i, name) }
		if m.OperationID == "" || len(m.OperationID) > 191 || !utf8.ValidString(m.OperationID) {
			problems.add(field("operation_id"), "An operation id is required.")
		}
		if m.ItemID == "" || len(m.ItemID) > 191 || !utf8.ValidString(m.ItemID) || syncControlChars.MatchString(m.ItemID) {
			problems.add(field("item_id"), "An item id is required.")
		}
		if m.BaseRevision != nil && *m.BaseRevision < 1 {
			problems.add(field("base_revision"), "A base revision is a positive number or null.")
		}
		switch m.Type {
		case "upsert":
			if m.Content == nil {
				problems.add(field("content"), "An upsert carries content.")
				continue
			}
			if m.Kind == "binary" || m.Content.Encoding == "base64" {
				problems.add(field("path"), syncRefusedKind)
				continue
			}
			if m.Kind != "text" || m.Content.Encoding != "utf8" {
				problems.add(field("content"), "Content is utf8 text.")
				continue
			}
			data := []byte(m.Content.Data)
			if int64(len(data)) != m.Content.ByteLength || contentSha256(data) != m.Content.Sha256 {
				problems.add(field("content"), "The content does not match its sha256 and byte_length.")
				continue
			}
			if m.Content.MediaType == "" {
				problems.add(field("content"), "A media type is required.")
				continue
			}
			if message := v.syncPathProblem(m.Path, true); message != "" {
				problems.add(field("path"), message)
				continue
			}
			if !syncTextKind(m.Path, data) {
				problems.add(field("path"), syncRefusedKind)
			} else if int64(len(data)) > v.maxSyncText {
				problems.add(field("path"), syncRefusedSize)
			}
		case "move":
			if m.BaseRevision == nil {
				problems.add(field("base_revision"), "A move names the revision it moves.")
			}
			if message := v.syncPathProblem(m.Path, true); message != "" {
				problems.add(field("path"), message)
			}
		case "delete":
			if m.BaseRevision == nil {
				problems.add(field("base_revision"), "A delete names the revision it deletes.")
			}
		default:
			problems.add(field("type"), "The type is upsert, move or delete.")
		}
	}
	if len(problems.Fields) == 0 {
		return nil
	}
	return problems
}

// syncPathProblem says why a path cannot hold a synced file, or "".
func (v *Vault) syncPathProblem(p string, text bool) string {
	normalized, err := normalizeSyncPath(p)
	if err != nil || normalized != p {
		return syncRefusedName
	}
	if !shouldSyncPath(p) {
		return syncRefusedNotSync
	}
	if runtime.GOOS == "windows" && syncWindowsUnsafe(p) {
		return syncRefusedName
	}
	if text && !syncHasTextExtension(p) {
		return syncRefusedKind
	}
	if _, err := SafeDestination(v.root, p); err != nil {
		return syncRefusedPlace
	}
	return ""
}

// SyncMutate applies a batch: each mutation lands only if its item is still
// at the revision it was built on, checked under the vault lock, and each is
// answered on its own. A repeated operation id gets its first answer.
func (v *Vault) SyncMutate(mutations []SyncMutation) (SyncMutationResult, error) {
	if problems := validateSyncMutations(v, mutations); problems != nil {
		return SyncMutationResult{}, problems
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.syncEnsureLocked(); err != nil {
		return SyncMutationResult{}, err
	}
	s := v.sync
	result := SyncMutationResult{VaultID: s.vaultID, Acknowledged: []SyncAck{}, Conflicts: []SyncConflict{}}
	touched, settings := false, false
	finish := func(err error) (SyncMutationResult, error) {
		if touched {
			v.invalidateTextSearchCache()
			if settings {
				v.invalidateSettingsCache()
			}
		}
		if flushErr := s.flush(); flushErr != nil {
			v.syncFailedLocked(flushErr)
			return SyncMutationResult{}, fmt.Errorf("%w: %v", ErrSyncUnavailable, flushErr)
		}
		if err != nil {
			var refusal syncRefusal
			if errors.As(err, &refusal) {
				problems := &SyncValidationError{}
				problems.add(fmt.Sprintf("mutations.%d.path", refusal.index), refusal.message)
				return SyncMutationResult{}, problems
			}
			return SyncMutationResult{}, err
		}
		result.Cursor = s.seq
		return result, nil
	}
	for i, m := range mutations {
		if prior := s.ops[m.OperationID]; prior != nil {
			syncAnswer(&result, prior)
			continue
		}
		rec, err := v.syncApplyLocked(i, m)
		if err != nil {
			return finish(err)
		}
		if rec.Kind == "c" {
			touched = true
			settings = settings || strings.EqualFold(rec.Path, internalVaultDir+"/"+vaultSettingsFile) ||
				strings.EqualFold(rec.PreviousPath, internalVaultDir+"/"+vaultSettingsFile)
		}
		syncAnswer(&result, &rec)
	}
	return finish(nil)
}

func syncAnswer(result *SyncMutationResult, rec *syncRecord) {
	if rec.Kind == "c" {
		result.Acknowledged = append(result.Acknowledged, SyncAck{
			OperationID: rec.OperationID, ItemID: rec.ItemID, Revision: rec.Revision, Sequence: rec.Seq,
		})
		return
	}
	result.Conflicts = append(result.Conflicts, SyncConflict{
		OperationID: rec.OperationID, ItemID: rec.ItemID, Code: rec.Code,
		CurrentRevision: rec.CurrentRevision, CurrentPath: rec.CurrentPath,
	})
}

// syncApplyLocked applies one mutation and returns the record that answers
// it: a change record ("c") when it landed, a conflict record ("x") when not.
func (v *Vault) syncApplyLocked(index int, m SyncMutation) (syncRecord, error) {
	s := v.sync
	// Until the index has seen what is on disk, compare the disk: an edit made
	// outside the server is recorded first, which makes this mutation's base
	// stale instead of writing over that edit. The same goes for whatever
	// holds the destination.
	scope := syncScope{}
	if item := s.items[m.ItemID]; item != nil {
		scope.paths = append(scope.paths, item.Path)
	}
	if m.Type != "delete" {
		scope.paths = append(scope.paths, m.Path)
		if holder := s.byKey[syncPathKey(m.Path)]; holder != nil {
			scope.paths = append(scope.paths, holder.Path)
		}
	}
	if err := v.syncReconcileLocked(scope); err != nil {
		return syncRecord{}, err
	}

	item := s.items[m.ItemID]
	conflict := func(code string) (syncRecord, error) {
		rec := syncRecord{OperationID: m.OperationID, ItemID: m.ItemID, Code: code}
		if item != nil {
			revision, current := item.Revision, item.Path
			rec.CurrentRevision, rec.CurrentPath = &revision, &current
		} else if tomb, ok := s.tombstones[m.ItemID]; ok {
			revision := tomb.Revision
			rec.CurrentRevision = &revision
		}
		s.emitConflict(rec)
		return s.conflicts[len(s.conflicts)-1], nil
	}
	if m.BaseRevision == nil {
		// Only an upsert gets here (validation): a new file.
		if item != nil {
			return conflict("REVISION_CONFLICT")
		}
		if _, deleted := s.tombstones[m.ItemID]; deleted {
			return conflict("ITEM_DELETED")
		}
	} else {
		if item == nil {
			return conflict("ITEM_DELETED")
		}
		if item.Revision != *m.BaseRevision {
			return conflict("REVISION_CONFLICT")
		}
	}
	if m.Type != "delete" {
		if holder := s.byKey[syncPathKey(m.Path)]; holder != nil && holder != item {
			return conflict("PATH_CONFLICT")
		}
	}

	switch m.Type {
	case "delete":
		abs := filepath.Join(v.root, filepath.FromSlash(item.Path))
		if err := renameWithRetry(abs, "", func(from, _ string) error { return os.Remove(from) }, time.Sleep); err != nil && !errors.Is(err, os.ErrNotExist) {
			return syncRecord{}, err
		}
		syncDir(filepath.Dir(abs))
		v.syncPruneEmptyParents(item.Path)
		return s.emit(syncRecord{ItemID: item.ItemID, Type: "delete", Path: item.Path, Revision: item.Revision + 1, OperationID: m.OperationID}), nil
	case "move":
		previous := item.Path
		if err := v.syncRenameLocked(index, previous, m.Path); err != nil {
			return syncRecord{}, err
		}
		v.syncPruneEmptyParents(previous)
		v.syncDirsOf(previous, m.Path)
		return s.emit(syncRecord{
			ItemID: item.ItemID, Type: "move", Path: m.Path, PreviousPath: previous,
			Revision: item.Revision + 1, MtimeNs: v.syncMtime(m.Path), OperationID: m.OperationID,
		}), nil
	}

	data := []byte(m.Content.Data)
	rec := syncRecord{
		ItemID: m.ItemID, Type: "upsert", Path: m.Path, Sha256: m.Content.Sha256,
		ByteLength: int64(len(data)), MediaType: syncMediaType(m.Path), OperationID: m.OperationID, Revision: 1,
	}
	if item != nil {
		rec.Revision = item.Revision + 1
		if item.Path != m.Path {
			// Rename first, then write: a case-only rename on a
			// case-insensitive disk is the same file under its new name.
			if err := v.syncRenameLocked(index, item.Path, m.Path); err != nil {
				return syncRecord{}, err
			}
			rec.PreviousPath = item.Path
			v.syncPruneEmptyParents(item.Path)
		}
	}
	if err := s.writeBlob(rec.Sha256, data); err != nil {
		return syncRecord{}, err
	}
	abs := filepath.Join(v.root, filepath.FromSlash(m.Path))
	if info, err := os.Lstat(abs); err == nil && (item == nil || info.IsDir()) {
		// A new file's name is free in the index, so anything already there
		// is a folder or a file sync does not carry (not UTF-8, too large).
		// It is never written over.
		return syncRecord{}, syncRefusal{index: index, message: syncRefusedPlace}
	}
	if err := writeFileAtomic(abs, data, v.fileMode, v.dirMode); err != nil {
		if isNotDirectory(err) {
			return syncRecord{}, syncRefusal{index: index, message: syncRefusedPlace}
		}
		return syncRecord{}, err
	}
	if rec.PreviousPath != "" {
		v.syncDirsOf(rec.PreviousPath, m.Path)
	} else {
		v.syncDirsOf(m.Path)
	}
	rec.MtimeNs = v.syncMtime(m.Path)
	return s.emit(rec), nil
}

// syncDir makes a rename or remove in dir durable. The log record that
// follows is fsynced, and on some filesystems (btrfs, so a Synology NAS) that
// does not carry an earlier rename with it: a power cut could keep the record
// and lose the file change, and the next reconcile would tell every copy to
// undo a write it was acknowledged for. Windows has no directory fsync; NTFS
// journals the rename itself.
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = f.Sync()
	_ = f.Close()
}

func (v *Vault) syncDirsOf(rels ...string) {
	seen := map[string]bool{}
	for _, rel := range rels {
		dir := filepath.Dir(filepath.Join(v.root, filepath.FromSlash(rel)))
		if !seen[dir] {
			seen[dir] = true
			syncDir(dir)
		}
	}
}

// syncWindowsUnsafe reports a name Windows cannot hold as a file: a reserved
// device name (CON, NUL, COM1 and the like, with any extension) or a segment
// ending in a dot or a space.
func syncWindowsUnsafe(p string) bool {
	for _, segment := range strings.Split(p, "/") {
		if strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, " ") {
			return true
		}
		stem := strings.ToUpper(segment)
		if dot := strings.IndexByte(stem, '.'); dot >= 0 {
			stem = stem[:dot]
		}
		switch stem {
		case "CON", "PRN", "AUX", "NUL":
			return true
		}
		if len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9' {
			return true
		}
	}
	return false
}

func (v *Vault) syncMtime(rel string) int64 {
	info, err := os.Stat(filepath.Join(v.root, filepath.FromSlash(rel)))
	if err != nil {
		return 0
	}
	obs := syncObs{mtimeNs: info.ModTime().UnixNano()}
	return obs.trustedMtime()
}

func isNotDirectory(err error) bool {
	return errors.Is(err, syscall.ENOTDIR) || strings.Contains(strings.ToLower(err.Error()), "not a directory")
}

func (v *Vault) syncRenameLocked(index int, from, to string) error {
	fromAbs := filepath.Join(v.root, filepath.FromSlash(from))
	toAbs := filepath.Join(v.root, filepath.FromSlash(to))
	if from == to {
		return nil
	}
	if target, err := os.Lstat(toAbs); err == nil {
		// The index says the name is free, so what is there is the same file
		// under another spelling (a case-insensitive disk), or a directory or
		// file sync does not carry. Never move over the second kind.
		source, sourceErr := os.Lstat(fromAbs)
		if sourceErr != nil || !os.SameFile(source, target) {
			return syncRefusal{index: index, message: syncRefusedPlace}
		}
	}
	if err := os.MkdirAll(filepath.Dir(toAbs), v.dirMode); err != nil {
		if isNotDirectory(err) || errors.Is(err, syscall.EEXIST) {
			return syncRefusal{index: index, message: syncRefusedPlace}
		}
		return err
	}
	return renameWithRetry(fromAbs, toAbs, os.Rename, time.Sleep)
}

// syncPruneEmptyParents removes the folders a sync move or delete left empty,
// as they are gone from the copy that sent it. It stops at the vault root, the
// system folders (wherever vault.json maps them), the attachments folders and
// .zennotes, and never removes a folder that still holds anything.
func (v *Vault) syncPruneEmptyParents(rel string) {
	keep := map[string]struct{}{v.root: {}}
	for _, folder := range []NoteFolder{FolderInbox, FolderQuick, FolderArchive, FolderTrash} {
		if root, err := v.folderRoot(folder); err == nil {
			keep[filepath.Clean(root)] = struct{}{}
		}
	}
	for _, dir := range []string{AssetsDir, PrimaryAttachmentsDir, "_assets", internalVaultDir} {
		keep[filepath.Join(v.root, dir)] = struct{}{}
	}
	dir := path.Dir(rel)
	for dir != "." && dir != "" && dir != "/" {
		abs := filepath.Join(v.root, filepath.FromSlash(dir))
		if _, protected := keep[abs]; protected {
			return
		}
		if strings.HasPrefix(dir, internalVaultDir+"/") && !strings.Contains(strings.TrimPrefix(dir, internalVaultDir+"/"), "/") {
			return // .zennotes/comments, .zennotes/templates and the like
		}
		if err := os.Remove(abs); err != nil {
			return // not empty, or not ours to remove
		}
		dir = path.Dir(dir)
	}
}
