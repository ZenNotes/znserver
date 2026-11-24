package vault

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The sync store: what the offline copy's protocol (#1108, phase 2) knows
// about the vault, kept in the vault at .zennotes/sync/ so it follows a vault
// switch or a vault moved to another server.
//
//	vault_id     this vault's sync identity. A new one means "rebuild".
//	index.json   a snapshot of the index (path -> item id, revision, sha256,
//	             mtime, size) and the tombstones, as of `sequence`.
//	log.jsonl    the append-only change log, one JSON record per line, fsynced
//	             per batch. Change records carry a sequence; conflict records
//	             carry the answer an operation got, so a repeated operation_id
//	             answers the same. Both are kept for the retention window
//	             (90 days by default); `floor` is the last sequence pruned.
//	blobs/       the bytes of every upsert the log still holds and of every
//	             item's current version, gzipped and named by sha256, so the
//	             change feed and revision reads (supportsSync 2) can serve
//	             any retained revision. The current versions matter most: a
//	             conflict's base is the revision a copy last synced, often
//	             one the store started with or one older than the log, and
//	             by the time an edit is noticed the file no longer holds it.
//	.gitignore   `*`, for vaults kept in Git.
//
// The log is the authority; index.json is a checkpoint that saves replaying
// the whole log at startup. Every field below is guarded by the vault's own
// lock (v.mu): writes under the write lock, reads under the read lock.
//
// The store starts the first time a client uses the protocol (or exists
// already on disk). A vault nobody syncs pays nothing for it.

const (
	syncFormatVersion        = 1
	syncVaultIDFile          = "vault_id"
	syncIndexFile            = "index.json"
	syncLogFile              = "log.jsonl"
	syncBlobsDir             = "blobs"
	syncCheckpointEvery      = 256
	DefaultSyncLogRetention  = 90 * 24 * time.Hour
	syncRacyWindow           = 2 * time.Second
	syncMinReconcileInterval = 15 * time.Second
)

// ErrSyncCursorExpired answers a cursor older than the retained log (or newer
// than the log has ever been): the client rebuilds from the manifest.
var ErrSyncCursorExpired = errors.New("sync cursor is older than the retained change log")

// ErrSyncUnavailable answers while the store could not be read or written.
var ErrSyncUnavailable = errors.New("sync store unavailable")

type syncItem struct {
	ItemID     string `json:"item_id"`
	Path       string `json:"path"`
	Revision   int64  `json:"revision"`
	Sha256     string `json:"sha256"`
	ByteLength int64  `json:"byte_length"`
	MediaType  string `json:"media_type"`
	// MtimeNs is the file's mtime when it was last hashed, or 0 when that
	// mtime was too recent to trust ("racily clean", as Git calls it): a
	// write in the same clock tick with the same size would not show.
	MtimeNs int64 `json:"mtime_ns"`
}

type syncTombstone struct {
	Revision int64 `json:"revision"`
	At       int64 `json:"at"`
}

// syncRecord is one line of log.jsonl.
type syncRecord struct {
	// Kind is "c" for a change and "x" for a conflict answer.
	Kind         string `json:"k"`
	Seq          int64  `json:"seq,omitempty"`
	At           int64  `json:"at"`
	ItemID       string `json:"item_id"`
	Type         string `json:"type,omitempty"`
	Path         string `json:"path,omitempty"`
	PreviousPath string `json:"previous_path,omitempty"`
	Revision     int64  `json:"revision,omitempty"`
	// The bytes the item holds as of this record: an upsert's, and (from
	// revision reads on) the bytes a move carried along.
	Sha256 string `json:"sha256,omitempty"`
	// The bytes the item held just before an upsert or a delete (from
	// revision reads on), so the revision before it can be read even when it
	// has no record of its own, as revision 1 of a file the store started
	// with does not.
	BaseSha256  string `json:"base_sha256,omitempty"`
	ByteLength  int64  `json:"byte_length,omitempty"`
	MediaType   string `json:"media_type,omitempty"`
	MtimeNs     int64  `json:"mtime_ns,omitempty"`
	OperationID string `json:"op,omitempty"`
	// Conflict answers only.
	Code            string  `json:"code,omitempty"`
	CurrentRevision *int64  `json:"current_revision,omitempty"`
	CurrentPath     *string `json:"current_path,omitempty"`
}

type syncSnapshot struct {
	Version    int                      `json:"version"`
	VaultID    string                   `json:"vault_id"`
	Sequence   int64                    `json:"sequence"`
	Floor      int64                    `json:"floor"`
	Items      []syncItem               `json:"items"`
	Tombstones map[string]syncTombstone `json:"tombstones"`
}

type syncStore struct {
	vaultRoot string
	dir       string
	fileMode  fs.FileMode
	dirMode   fs.FileMode
	maxText   int64
	retention time.Duration

	vaultID    string
	seq        int64 // the newest sequence
	floor      int64 // the newest sequence pruned; changes[0].Seq == floor+1
	snapSeq    int64 // the sequence index.json reflects
	items      map[string]*syncItem
	byPath     map[string]*syncItem
	byKey      map[string]*syncItem
	tombstones map[string]syncTombstone
	changes    []syncRecord
	conflicts  []syncRecord
	ops        map[string]*syncRecord

	pending   []syncRecord
	broken    bool // a flush failed; the store must be reloaded from disk
	dirty     bool // index changed without a record (mtimes only)
	sinceSnap int

	lastReconcile time.Time
	lastPrune     time.Time
	// blobsChecked is set once a full reconcile has made sure every item's
	// current version has a blob, so a store from before that rule (server
	// 2.91.1) fills them in once per load rather than on every reconcile.
	blobsChecked bool
}

func newSyncStoreShell(vaultRoot string, fileMode, dirMode fs.FileMode, maxText int64, retention time.Duration) *syncStore {
	if retention <= 0 {
		retention = DefaultSyncLogRetention
	}
	return &syncStore{
		vaultRoot:  vaultRoot,
		dir:        filepath.Join(vaultRoot, filepath.FromSlash(syncStoreRelDir)),
		fileMode:   fileMode,
		dirMode:    dirMode,
		maxText:    maxText,
		retention:  retention,
		items:      map[string]*syncItem{},
		byPath:     map[string]*syncItem{},
		byKey:      map[string]*syncItem{},
		tombstones: map[string]syncTombstone{},
		ops:        map[string]*syncRecord{},
	}
}

func randomSyncID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on supported platforms; a clock-based id
		// is still unique enough for one vault.
		return fmt.Sprintf("%032x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func (s *syncStore) path(name string) string { return filepath.Join(s.dir, name) }

// syncStoreExists reports whether a vault already has a store on disk.
func syncStoreExists(vaultRoot string) bool {
	_, err := os.Stat(filepath.Join(vaultRoot, filepath.FromSlash(syncStoreRelDir), syncVaultIDFile))
	return err == nil
}

// errSyncDamaged marks a store whose files say something impossible (a
// missing or unparsable snapshot, a gap in the log). Only that kind of
// failure starts the store over; an I/O error (EMFILE, EACCES) is retried, so
// a passing problem never costs every copy a rebuild.
var errSyncDamaged = errors.New("sync store is damaged")

func syncDamaged(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errSyncDamaged, fmt.Sprintf(format, args...))
}

// load reads the store from disk. A store that cannot be trusted comes back as
// errSyncDamaged, and the caller starts over with a new vault id, which every
// client answers by rebuilding.
func (s *syncStore) load() error {
	rawID, err := os.ReadFile(s.path(syncVaultIDFile))
	if err != nil {
		return err
	}
	s.vaultID = strings.TrimSpace(string(rawID))
	if s.vaultID == "" {
		return syncDamaged("empty vault_id")
	}
	rawSnap, err := os.ReadFile(s.path(syncIndexFile))
	if errors.Is(err, os.ErrNotExist) {
		return syncDamaged("index.json is missing")
	}
	if err != nil {
		return err
	}
	var snap syncSnapshot
	if err := json.Unmarshal(rawSnap, &snap); err != nil {
		return syncDamaged("index.json: %v", err)
	}
	if snap.Version != syncFormatVersion || snap.VaultID != s.vaultID {
		return syncDamaged("index.json belongs to another store")
	}
	s.items, s.byPath, s.byKey = map[string]*syncItem{}, map[string]*syncItem{}, map[string]*syncItem{}
	for i := range snap.Items {
		item := snap.Items[i]
		s.indexItem(&item)
	}
	s.tombstones = snap.Tombstones
	if s.tombstones == nil {
		s.tombstones = map[string]syncTombstone{}
	}
	s.floor, s.snapSeq, s.seq = snap.Floor, snap.Sequence, snap.Sequence
	if s.snapSeq < s.floor {
		return syncDamaged("index.json is older than the log's floor")
	}
	return s.loadLog()
}

func (s *syncStore) loadLog() error {
	f, err := os.OpenFile(s.path(syncLogFile), os.O_RDWR|os.O_CREATE, s.fileMode)
	if err != nil {
		return err
	}
	defer f.Close()
	reader := bufio.NewReaderSize(f, 1<<16)
	var good int64
	expected := s.floor + 1
	s.changes, s.conflicts, s.ops = nil, nil, map[string]*syncRecord{}
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			var rec syncRecord
			if err := json.Unmarshal(line, &rec); err != nil {
				return syncDamaged("log.jsonl: damaged record after byte %d", good)
			}
			good += int64(len(line))
			switch rec.Kind {
			case "c":
				if rec.Seq <= s.floor {
					// A prune checkpointed the index and crashed before it
					// rewrote the log: what it was dropping is still here.
					continue
				}
				if rec.Seq != expected {
					return syncDamaged("log.jsonl: expected sequence %d, found %d", expected, rec.Seq)
				}
				expected++
				if rec.Seq > s.snapSeq {
					s.applyRecord(rec)
				}
				s.changes = append(s.changes, rec)
				if rec.OperationID != "" {
					s.ops[rec.OperationID] = &s.changes[len(s.changes)-1]
				}
			case "x":
				s.conflicts = append(s.conflicts, rec)
			default:
				return syncDamaged("log.jsonl: unknown record kind %q", rec.Kind)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	s.reindexOps()
	if expected-1 < s.snapSeq {
		return syncDamaged("log.jsonl ends before index.json")
	}
	s.seq = expected - 1
	// A torn last line (a crash in the middle of an append) never reached a
	// client: an answer goes out only after its batch is fsynced.
	if info, err := f.Stat(); err == nil && info.Size() != good {
		if err := f.Truncate(good); err != nil {
			return err
		}
	}
	return nil
}

func (s *syncStore) reindexOps() {
	s.ops = map[string]*syncRecord{}
	for i := range s.changes {
		if op := s.changes[i].OperationID; op != "" {
			s.ops[op] = &s.changes[i]
		}
	}
	for i := range s.conflicts {
		if op := s.conflicts[i].OperationID; op != "" {
			s.ops[op] = &s.conflicts[i]
		}
	}
}

// initialize creates a new store for the vault as it is: every synced file
// becomes an item at revision 1, with no log behind it (a client bootstraps
// from the manifest at cursor 0, and nothing older can be asked for).
func (s *syncStore) initialize(v *Vault) error {
	if err := os.RemoveAll(s.dir); err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, s.dirMode); err != nil {
		return err
	}
	if err := os.WriteFile(s.path(".gitignore"), []byte("# ZenNotes sync state (offline copies of this vault). Not for Git.\n*\n"), s.fileMode); err != nil {
		return err
	}
	s.vaultID = randomSyncID()
	s.seq, s.floor, s.snapSeq = 0, 0, 0
	s.items, s.byPath, s.byKey = map[string]*syncItem{}, map[string]*syncItem{}, map[string]*syncItem{}
	s.tombstones = map[string]syncTombstone{}
	s.changes, s.conflicts, s.ops, s.pending = nil, nil, map[string]*syncRecord{}, nil
	observed, _, err := v.syncObserve(syncScope{all: true})
	if err != nil {
		return err
	}
	paths := make([]string, 0, len(observed))
	for p := range observed {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		obs := observed[p]
		if err := obs.hash(); err != nil || !obs.text {
			continue
		}
		if _, taken := s.byKey[syncPathKey(p)]; taken {
			log.Printf("sync: %s is the same name as another file on a case-insensitive disk; it is not synced", p)
			obs.data = nil
			continue
		}
		// The bytes every copy starts from, kept so a revision read can
		// answer revision 1 after the file changes. A failure here is not
		// fatal: the first full reconcile fills in what is missing.
		_ = s.writeBlob(obs.sha, obs.data)
		obs.data = nil
		s.indexItem(&syncItem{
			ItemID: randomSyncID(), Path: p, Revision: 1, Sha256: obs.sha,
			ByteLength: obs.size, MediaType: syncMediaType(p), MtimeNs: obs.trustedMtime(),
		})
	}
	if err := writeFileAtomic(s.path(syncLogFile), nil, s.fileMode, s.dirMode); err != nil {
		return err
	}
	if err := s.writeSnapshot(); err != nil {
		return err
	}
	// vault_id last: its presence is what marks the store as complete.
	if err := writeFileAtomic(s.path(syncVaultIDFile), []byte(s.vaultID+"\n"), s.fileMode, s.dirMode); err != nil {
		return err
	}
	s.lastReconcile = time.Now()
	s.lastPrune = time.Now()
	return nil
}

func (s *syncStore) writeSnapshot() error {
	snap := syncSnapshot{
		Version: syncFormatVersion, VaultID: s.vaultID, Sequence: s.seq, Floor: s.floor,
		Items: make([]syncItem, 0, len(s.items)), Tombstones: s.tombstones,
	}
	for _, item := range s.items {
		snap.Items = append(snap.Items, *item)
	}
	sort.Slice(snap.Items, func(i, j int) bool { return snap.Items[i].Path < snap.Items[j].Path })
	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(s.path(syncIndexFile), data, s.fileMode, s.dirMode); err != nil {
		return err
	}
	s.snapSeq, s.sinceSnap, s.dirty = s.seq, 0, false
	return nil
}

func (s *syncStore) indexItem(item *syncItem) {
	s.items[item.ItemID] = item
	s.byPath[item.Path] = item
	s.byKey[syncPathKey(item.Path)] = item
}

func (s *syncStore) unindexPath(item *syncItem) {
	if s.byPath[item.Path] == item {
		delete(s.byPath, item.Path)
	}
	if key := syncPathKey(item.Path); s.byKey[key] == item {
		delete(s.byKey, key)
	}
}

// applyRecord folds one change into the index, the same way at load time and
// when it is first recorded.
func (s *syncStore) applyRecord(rec syncRecord) {
	switch rec.Type {
	case "delete":
		if item := s.items[rec.ItemID]; item != nil {
			s.unindexPath(item)
			delete(s.items, rec.ItemID)
		}
		s.tombstones[rec.ItemID] = syncTombstone{Revision: rec.Revision, At: rec.At}
	case "move":
		if item := s.items[rec.ItemID]; item != nil {
			s.unindexPath(item)
			item.Path, item.Revision, item.MtimeNs = rec.Path, rec.Revision, rec.MtimeNs
			s.indexItem(item)
		}
	default:
		item := s.items[rec.ItemID]
		if item == nil {
			item = &syncItem{ItemID: rec.ItemID}
		} else {
			s.unindexPath(item)
		}
		item.Path, item.Revision = rec.Path, rec.Revision
		item.Sha256, item.ByteLength, item.MediaType, item.MtimeNs = rec.Sha256, rec.ByteLength, rec.MediaType, rec.MtimeNs
		s.indexItem(item)
	}
}

// emit records a change: it gets the next sequence, lands in the index at
// once, and reaches disk with the batch at flush.
func (s *syncStore) emit(rec syncRecord) syncRecord {
	if prior := s.items[rec.ItemID]; prior != nil {
		if rec.Type == "move" {
			rec.Sha256, rec.MediaType = prior.Sha256, prior.MediaType
		} else {
			rec.BaseSha256 = prior.Sha256
		}
	}
	s.seq++
	rec.Kind, rec.Seq = "c", s.seq
	if rec.At == 0 {
		rec.At = time.Now().UnixMilli()
	}
	s.applyRecord(rec)
	s.changes = append(s.changes, rec)
	s.pending = append(s.pending, rec)
	if rec.OperationID != "" {
		s.ops[rec.OperationID] = &s.changes[len(s.changes)-1]
	}
	return rec
}

func (s *syncStore) emitConflict(rec syncRecord) {
	rec.Kind, rec.At = "x", time.Now().UnixMilli()
	s.conflicts = append(s.conflicts, rec)
	s.pending = append(s.pending, rec)
	if rec.OperationID != "" {
		s.ops[rec.OperationID] = &s.conflicts[len(s.conflicts)-1]
	}
}

// flush appends the batch to the log and fsyncs it. Nothing a batch recorded
// is answered to a client before this returns.
//
// A failed append is cut back off the log, so what reaches the disk is always
// whole batches, and the store refuses every later flush until it is reloaded
// (the in-memory state now holds records the disk does not).
func (s *syncStore) flush() error {
	if s.broken {
		return errors.New("sync store needs a reload after a failed write")
	}
	if len(s.pending) > 0 {
		var buf bytes.Buffer
		for _, rec := range s.pending {
			line, err := json.Marshal(rec)
			if err != nil {
				return err
			}
			buf.Write(line)
			buf.WriteByte('\n')
		}
		f, err := os.OpenFile(s.path(syncLogFile), os.O_WRONLY|os.O_APPEND|os.O_CREATE, s.fileMode)
		if err != nil {
			s.broken = true
			return err
		}
		var before int64 = -1
		if info, statErr := f.Stat(); statErr == nil {
			before = info.Size()
		}
		fail := func(err error) error {
			s.broken = true
			if before >= 0 {
				_ = f.Truncate(before)
				_ = f.Sync()
			}
			_ = f.Close()
			return err
		}
		if _, err := f.Write(buf.Bytes()); err != nil {
			return fail(err)
		}
		if err := f.Sync(); err != nil {
			return fail(err)
		}
		if err := f.Close(); err != nil {
			s.broken = true
			return err
		}
		s.sinceSnap += len(s.pending)
		s.pending = nil
	}
	// Op pointers may point into an array an append has since outgrown. The
	// records there are never changed, so they stay correct.
	if s.sinceSnap >= syncCheckpointEvery {
		return s.writeSnapshot()
	}
	return nil
}

// --- Blobs ---

func (s *syncStore) blobPath(sha string) string {
	return filepath.Join(s.dir, syncBlobsDir, sha[:2], sha+".gz")
}

func (s *syncStore) writeBlob(sha string, data []byte) error {
	target := s.blobPath(sha)
	if _, err := os.Stat(target); err == nil {
		return nil
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if _, err := zw.Write(data); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return writeFileAtomic(target, buf.Bytes(), s.fileMode, s.dirMode)
}

func (s *syncStore) hasBlob(sha string) bool {
	_, err := os.Stat(s.blobPath(sha))
	return err == nil
}

func (s *syncStore) readBlob(sha string) ([]byte, error) {
	f, err := os.Open(s.blobPath(sha))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	data, err := io.ReadAll(io.LimitReader(zr, s.maxText+1))
	if err != nil {
		return nil, err
	}
	if contentSha256(data) != sha {
		return nil, fmt.Errorf("blob %s is damaged", sha)
	}
	return data, nil
}

// --- Retention ---

// prune drops log records older than the retention window, then the blobs no
// retained record and no current item names. The index is checkpointed first, so it never needs
// a record that is gone.
func (s *syncStore) prune(now time.Time) error {
	cutoff := now.Add(-s.retention).UnixMilli()
	drop := 0
	for drop < len(s.changes) && s.changes[drop].At < cutoff {
		drop++
	}
	keptConflicts := s.conflicts[:0:0]
	for _, rec := range s.conflicts {
		if rec.At >= cutoff {
			keptConflicts = append(keptConflicts, rec)
		}
	}
	for id, tomb := range s.tombstones {
		if tomb.At < cutoff {
			delete(s.tombstones, id)
		}
	}
	s.lastPrune = now
	if drop == 0 && len(keptConflicts) == len(s.conflicts) {
		return s.pruneBlobs()
	}
	if drop > 0 {
		s.floor = s.changes[drop-1].Seq
	}
	s.changes = append([]syncRecord(nil), s.changes[drop:]...)
	s.conflicts = keptConflicts
	s.reindexOps()
	if err := s.writeSnapshot(); err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, rec := range s.changes {
		line, _ := json.Marshal(rec)
		buf.Write(line)
		buf.WriteByte('\n')
	}
	for _, rec := range s.conflicts {
		line, _ := json.Marshal(rec)
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := writeFileAtomic(s.path(syncLogFile), buf.Bytes(), s.fileMode, s.dirMode); err != nil {
		return err
	}
	return s.pruneBlobs()
}

func (s *syncStore) pruneBlobs() error {
	keep := map[string]struct{}{}
	for _, rec := range s.changes {
		// What a retained record names, before and after it.
		for _, sha := range []string{rec.Sha256, rec.BaseSha256} {
			if sha != "" {
				keep[sha] = struct{}{}
			}
		}
	}
	for _, item := range s.items {
		keep[item.Sha256] = struct{}{}
	}
	root := filepath.Join(s.dir, syncBlobsDir)
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		sha := strings.TrimSuffix(d.Name(), ".gz")
		if _, ok := keep[sha]; !ok {
			_ = os.Remove(p)
		}
		return nil
	})
}
