package vault

import (
	"errors"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Reconcile brings the sync index in line with the files, for the whole vault
// or for the part of it one mutation touched. It is how revisions are bumped
// for every write path at once: each vault mutator, holding v.mu, names the
// paths it changed (and the moves it made, so a rename keeps its item id), and
// the same code that catches edits made with a shell, `git pull` or Syncthing
// works out what changed and records it.
//
// The fast path is a stat: a file whose mtime and size match the index is not
// read. Only what differs is hashed. A path that vanished and a new path with
// the same bytes are one move (the engine's rule, cloud-sync-engine.ts), and a
// mutator's own move hint settles which is which when several files share
// bytes (a folder of empty notes).

type syncScope struct {
	all   bool
	paths []string    // exact vault-relative paths
	trees []string    // vault-relative directories, everything under them
	moves [][2]string // from, to (a file or a directory)
}

func (scope syncScope) contains(p string) bool {
	if scope.all {
		return true
	}
	for _, exact := range scope.paths {
		if p == exact {
			return true
		}
	}
	for _, tree := range scope.trees {
		if tree == "" || p == tree || strings.HasPrefix(p, tree+"/") {
			return true
		}
	}
	return false
}

// syncObs is one file on disk that sync might carry, as a stat saw it.
type syncObs struct {
	rel     string
	abs     string
	size    int64
	mtimeNs int64
	sha     string
	text    bool
	data    []byte
	done    bool
}

func (o *syncObs) hash() error {
	if o.done {
		return nil
	}
	data, err := os.ReadFile(o.abs)
	if err != nil {
		return err
	}
	o.size, o.sha, o.text, o.data, o.done = int64(len(data)), contentSha256(data), syncTextKind(o.rel, data), data, true
	return nil
}

// trustedMtime is the mtime to keep in the index, or 0 when it is too recent
// to tell a later write in the same tick from this one.
func (o *syncObs) trustedMtime() int64 {
	if time.Since(time.Unix(0, o.mtimeNs)) < syncRacyWindow {
		return 0
	}
	return o.mtimeNs
}

func (v *Vault) syncRel(abs string) (string, bool) {
	rel, err := filepath.Rel(v.root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if rel == "." {
		return "", true
	}
	return filepath.ToSlash(rel), true
}

// syncThroughLink reports whether any directory on the way to rel is a link.
// The walk never follows one, so an exact-path look must not either, or the two
// would disagree about the file forever.
func (v *Vault) syncThroughLink(rel string) bool {
	parts := strings.Split(rel, "/")
	walk := v.root
	for _, part := range parts[:len(parts)-1] {
		walk = filepath.Join(walk, part)
		info, err := os.Lstat(walk)
		if err != nil {
			return false
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return true
		}
	}
	return false
}

func (v *Vault) syncCandidate(rel, abs string, info fs.FileInfo) *syncObs {
	if !info.Mode().IsRegular() || !shouldSyncPath(rel) || !syncHasTextExtension(rel) || IsAtomicWriteTempPath(abs) {
		return nil
	}
	if info.Size() > v.maxSyncText {
		return nil
	}
	return &syncObs{rel: rel, abs: abs, size: info.Size(), mtimeNs: info.ModTime().UnixNano()}
}

// syncBlind is what a walk could not see: paths (files or whole folders) it
// failed to read. Nothing under them counts as gone, since an unreadable
// folder is not an empty one and a delete would reach every copy.
type syncBlind []string

func (b syncBlind) covers(p string) bool {
	for _, blind := range b {
		if blind == "" || p == blind || strings.HasPrefix(p, blind+"/") {
			return true
		}
	}
	return false
}

// syncObserve lists, by stat only, the files in scope that sync could carry,
// and what it could not look at.
func (v *Vault) syncObserve(scope syncScope) (map[string]*syncObs, syncBlind, error) {
	observed := map[string]*syncObs{}
	var blind syncBlind
	notice := func(p string, err error) {
		if r, ok := v.syncRel(p); ok {
			log.Printf("sync: cannot read %s (%v); what it holds keeps its last synced version", p, err)
			blind = append(blind, r)
		}
	}
	walk := func(rel string) error {
		start := v.root
		if rel != "" {
			start = filepath.Join(v.root, filepath.FromSlash(rel))
			if v.syncThroughLink(rel + "/x") {
				return nil
			}
		}
		info, err := os.Lstat(start)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				notice(start, err)
			}
			return nil
		}
		if !info.IsDir() {
			return nil
		}
		return filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					notice(p, err)
				}
				if d != nil && d.IsDir() && p != start {
					return filepath.SkipDir
				}
				return nil
			}
			r, ok := v.syncRel(p)
			if !ok {
				return nil
			}
			if d.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			if d.IsDir() {
				if !shouldTraverseSyncDir(r) {
					return filepath.SkipDir
				}
				return nil
			}
			fileInfo, err := d.Info()
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					notice(p, err)
				}
				return nil
			}
			if obs := v.syncCandidate(r, p, fileInfo); obs != nil {
				observed[r] = obs
			}
			return nil
		})
	}
	if scope.all {
		return observed, blind, walk("")
	}
	for _, tree := range scope.trees {
		if err := walk(tree); err != nil {
			return nil, nil, err
		}
	}
	// On a case-insensitive disk a stat finds a file under any spelling, so
	// where two names one case apart are in play (a case-only rename, an
	// index entry under the other spelling) the directory says which is real.
	keys := map[string]int{}
	for _, rel := range scope.paths {
		keys[syncPathKey(rel)]++
	}
	for _, rel := range scope.paths {
		if rel == "" || observed[rel] != nil || v.syncThroughLink(rel) {
			continue
		}
		abs := filepath.Join(v.root, filepath.FromSlash(rel))
		info, err := os.Lstat(abs)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) && !isNotDirectory(err) {
				notice(abs, err)
			}
			continue
		}
		ambiguous := keys[syncPathKey(rel)] > 1
		if v.sync != nil {
			if holder := v.sync.byKey[syncPathKey(rel)]; holder != nil && holder.Path != rel {
				ambiguous = true
			}
		}
		if ambiguous && !syncExactName(abs) {
			continue
		}
		if obs := v.syncCandidate(rel, abs, info); obs != nil {
			observed[rel] = obs
		}
	}
	return observed, blind, nil
}

// syncExactName reports whether the directory lists abs under exactly its own
// spelling.
func syncExactName(abs string) bool {
	entries, err := os.ReadDir(filepath.Dir(abs))
	if err != nil {
		return false
	}
	name := filepath.Base(abs)
	for _, entry := range entries {
		if entry.Name() == name {
			return true
		}
	}
	return false
}

// syncUnreadable says a file could not be read, once per reconcile. The file
// keeps whatever the index said about it: an unreadable file is not a deleted
// one, and one bad file must never stop the rest of the vault from syncing.
func syncUnreadable(rel string, err error) {
	log.Printf("sync: cannot read %s (%v); it keeps its last synced version until it can be read", rel, err)
}

type syncPair struct {
	item *syncItem
	obs  *syncObs
}

// syncReconcileLocked records what changed inside scope. v.mu must be held
// for writing. The batch is fsynced before it returns.
func (v *Vault) syncReconcileLocked(scope syncScope) error {
	s := v.sync
	if s == nil {
		return nil
	}
	observed, blind, err := v.syncObserve(scope)
	if err != nil {
		return err
	}

	tracked := make([]*syncItem, 0)
	for _, item := range s.items {
		if scope.contains(item.Path) && !blind.covers(item.Path) {
			tracked = append(tracked, item)
		}
	}
	sort.Slice(tracked, func(i, j int) bool { return tracked[i].Path < tracked[j].Path })

	var vanished []*syncItem
	var changed []syncPair
	for _, item := range tracked {
		obs := observed[item.Path]
		if obs == nil {
			vanished = append(vanished, item)
			continue
		}
		delete(observed, item.Path)
		if item.MtimeNs != 0 && obs.mtimeNs == item.MtimeNs && obs.size == item.ByteLength {
			continue
		}
		if err := obs.hash(); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				vanished = append(vanished, item)
				continue
			}
			syncUnreadable(item.Path, err)
			continue
		}
		if !obs.text {
			// Turned into something phase 2 does not carry.
			vanished = append(vanished, item)
			continue
		}
		if obs.sha == item.Sha256 {
			if trusted := obs.trustedMtime(); item.MtimeNs != trusted {
				item.MtimeNs = trusted
				s.dirty = true
			}
			continue
		}
		changed = append(changed, syncPair{item, obs})
	}

	appeared := map[string]*syncObs{}
	for rel, obs := range observed {
		if err := obs.hash(); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				syncUnreadable(rel, err)
			}
			continue
		}
		if obs.text {
			appeared[rel] = obs
		}
	}

	// Pair what vanished with what appeared: the mutator's own hints first,
	// then bytes that match exactly one candidate.
	taken := map[*syncObs]bool{}
	paired := map[*syncItem]*syncObs{}
	var pairs []syncPair
	for _, item := range vanished {
		for _, move := range scope.moves {
			from, to := move[0], move[1]
			if item.Path != from && !strings.HasPrefix(item.Path, from+"/") {
				continue
			}
			candidate := to + item.Path[len(from):]
			if obs := appeared[candidate]; obs != nil && !taken[obs] {
				taken[obs], paired[item] = true, obs
				pairs = append(pairs, syncPair{item, obs})
				break
			}
		}
	}
	bySha := map[string][]*syncObs{}
	for _, obs := range appeared {
		bySha[obs.sha] = append(bySha[obs.sha], obs)
	}
	for _, item := range vanished {
		if paired[item] != nil {
			continue
		}
		var candidates []*syncObs
		for _, obs := range bySha[item.Sha256] {
			if !taken[obs] {
				candidates = append(candidates, obs)
			}
		}
		if len(candidates) > 1 {
			var sameName []*syncObs
			for _, obs := range candidates {
				if path.Base(obs.rel) == path.Base(item.Path) {
					sameName = append(sameName, obs)
				}
			}
			candidates = sameName
		}
		if len(candidates) == 1 {
			obs := candidates[0]
			taken[obs], paired[item] = true, obs
			pairs = append(pairs, syncPair{item, obs})
		}
	}

	fail := func(err error) error {
		if flushErr := s.flush(); flushErr != nil {
			return flushErr
		}
		return err
	}

	for _, item := range vanished {
		if paired[item] == nil {
			s.emit(syncRecord{ItemID: item.ItemID, Type: "delete", Path: item.Path, Revision: item.Revision + 1})
		}
	}
	// A move lands only once its destination's name is free, so a rename into
	// a name another moving file is leaving keeps both apart in the index.
	for len(pairs) > 0 {
		var waiting []syncPair
		for _, pair := range pairs {
			if holder := s.byKey[syncPathKey(pair.obs.rel)]; holder != nil && holder != pair.item {
				waiting = append(waiting, pair)
				continue
			}
			if err := v.syncEmitArrival(pair.item, pair.obs, ""); err != nil {
				return fail(err)
			}
		}
		if len(waiting) == len(pairs) {
			// A ring of renames through each other's names: record it as
			// deletes and new files rather than corrupt the index.
			for _, pair := range waiting {
				s.emit(syncRecord{ItemID: pair.item.ItemID, Type: "delete", Path: pair.item.Path, Revision: pair.item.Revision + 1})
				taken[pair.obs] = false
			}
			break
		}
		pairs = waiting
	}
	for _, pair := range changed {
		if err := v.syncEmitArrival(pair.item, pair.obs, ""); err != nil {
			return fail(err)
		}
	}
	var arrivals []string
	for rel, obs := range appeared {
		if !taken[obs] {
			arrivals = append(arrivals, rel)
		}
	}
	sort.Strings(arrivals)
	for _, rel := range arrivals {
		if holder := s.byKey[syncPathKey(rel)]; holder != nil {
			// Two names one case apart. On a case-insensitive disk an exact
			// look under another spelling finds the holder's own file, which
			// is nothing to say; a walk finding both is.
			if scope.all {
				log.Printf("sync: %s has the same name as %s on a case-insensitive disk; it is not synced", rel, holder.Path)
			}
			continue
		}
		if err := v.syncEmitArrival(nil, appeared[rel], randomSyncID()); err != nil {
			return fail(err)
		}
	}
	if scope.all {
		s.lastReconcile = time.Now()
	}
	if err := s.flush(); err != nil {
		return err
	}
	if scope.all && !s.blobsChecked {
		v.syncBackfillBlobs()
	}
	if scope.all && s.dirty {
		return s.writeSnapshot()
	}
	return nil
}

// syncBackfillBlobs gives every item's current version a blob, once per load.
// Only a store from before that rule (or one whose blob write failed) has
// any to write; for the rest this is a stat per item. A file that no longer
// holds the bytes the index names is skipped: the next reconcile records it
// as an edit, with a blob of its own.
func (v *Vault) syncBackfillBlobs() {
	s := v.sync
	failed := 0
	var firstErr error
	for _, item := range s.items {
		if s.hasBlob(item.Sha256) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(v.root, filepath.FromSlash(item.Path)))
		if err != nil || contentSha256(data) != item.Sha256 {
			continue
		}
		if err := s.writeBlob(item.Sha256, data); err != nil {
			if failed == 0 {
				firstErr = err
			}
			failed++
		}
	}
	if failed > 0 {
		log.Printf("sync: could not keep the current version of %d files for revision reads (%v); trying again at the next reconcile", failed, firstErr)
	}
	s.blobsChecked = failed == 0
}

// syncEmitArrival records obs as the new state of item: a move when only the
// path changed, else an upsert (carrying previous_path when it also moved). A
// nil item is a new file with the given id.
func (v *Vault) syncEmitArrival(item *syncItem, obs *syncObs, newID string) error {
	s := v.sync
	if item != nil && obs.sha == item.Sha256 {
		if obs.rel == item.Path {
			return nil
		}
		s.emit(syncRecord{
			ItemID: item.ItemID, Type: "move", Path: obs.rel, PreviousPath: item.Path,
			Revision: item.Revision + 1, MtimeNs: obs.trustedMtime(),
		})
		return nil
	}
	if err := s.writeBlob(obs.sha, obs.data); err != nil {
		return err
	}
	rec := syncRecord{
		Type: "upsert", Path: obs.rel, Sha256: obs.sha, ByteLength: obs.size,
		MediaType: syncMediaType(obs.rel), MtimeNs: obs.trustedMtime(),
	}
	if item == nil {
		rec.ItemID, rec.Revision = newID, 1
	} else {
		rec.ItemID, rec.Revision = item.ItemID, item.Revision+1
		if item.Path != obs.rel {
			rec.PreviousPath = item.Path
		}
	}
	s.emit(rec)
	obs.data = nil
	return nil
}

// --- What the vault's mutators call, holding v.mu for writing ---

// syncRecordLocked records a mutator's change. It never fails the mutation:
// the file is already written, and anything a failure here leaves out the next
// reconcile records as an edit made outside the server, so a client sees an
// extra revision, never a lost one.
func (v *Vault) syncRecordLocked(scope syncScope) {
	if v.sync == nil {
		return
	}
	if err := v.syncReconcileLocked(scope); err != nil {
		v.syncFailedLocked(err)
	}
}

// syncTouched records files a mutator created, wrote or removed.
func (v *Vault) syncTouched(abs ...string) {
	if v.sync == nil {
		return
	}
	scope := syncScope{}
	for _, a := range abs {
		if rel, ok := v.syncRel(a); ok && rel != "" {
			scope.paths = append(scope.paths, rel)
		}
	}
	if len(scope.paths) > 0 {
		v.syncRecordLocked(scope)
	}
}

// syncTreeTouched records everything under directories a mutator created or
// removed whole.
func (v *Vault) syncTreeTouched(abs ...string) {
	if v.sync == nil {
		return
	}
	scope := syncScope{}
	for _, a := range abs {
		if rel, ok := v.syncRel(a); ok {
			scope.trees = append(scope.trees, rel)
		}
	}
	if len(scope.trees) > 0 {
		v.syncRecordLocked(scope)
	}
}

// syncMoved records a file or directory a mutator moved, keeping each item's
// id across the move. Other paths the same mutator touched can ride along.
func (v *Vault) syncMoved(fromAbs, toAbs string, alsoAbs ...string) {
	if v.sync == nil {
		return
	}
	from, okFrom := v.syncRel(fromAbs)
	to, okTo := v.syncRel(toAbs)
	if !okFrom || !okTo || from == "" || to == "" {
		v.syncTreeTouched(v.root)
		return
	}
	scope := syncScope{moves: [][2]string{{from, to}}}
	if info, err := os.Lstat(toAbs); err == nil && info.IsDir() {
		scope.trees = []string{from, to}
	} else {
		scope.paths = []string{from, to}
	}
	for _, a := range alsoAbs {
		if rel, ok := v.syncRel(a); ok && rel != "" {
			scope.paths = append(scope.paths, rel)
		}
	}
	v.syncRecordLocked(scope)
}

// syncReplaced records a file written at toAbs in place of fromAbs (a renamed
// template or workflow): one item, moved, and changed if its bytes did. An
// empty or equal fromAbs is a plain write.
func (v *Vault) syncReplaced(fromAbs, toAbs string) {
	if fromAbs == "" || fromAbs == toAbs {
		v.syncTouched(toAbs)
		return
	}
	v.syncMoved(fromAbs, toAbs)
}

// syncMovedMany is syncMoved for a mutator that moved several things at once.
func (v *Vault) syncMovedMany(moves [][2]string) {
	if v.sync == nil || len(moves) == 0 {
		return
	}
	scope := syncScope{}
	for _, move := range moves {
		from, okFrom := v.syncRel(move[0])
		to, okTo := v.syncRel(move[1])
		if !okFrom || !okTo || from == "" || to == "" {
			continue
		}
		scope.moves = append(scope.moves, [2]string{from, to})
		scope.trees = append(scope.trees, from, to)
		scope.paths = append(scope.paths, from, to)
	}
	v.syncRecordLocked(scope)
}
