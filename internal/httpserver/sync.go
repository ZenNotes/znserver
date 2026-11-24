package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ZenNotes/znserver/internal/vault"
	"github.com/ZenNotes/znserver/internal/watcher"
)

// The offline copy's sync protocol (#1108, phase 2): the subset of ZenNotes
// Cloud's sync API that its engine needs to keep a local mirror of the vault
// this server serves. The vault package does the work; this file is the wire.
//
//	GET  /api/sync/manifest?page=&perPage=&include_content=
//	GET  /api/sync/changes?after=&limit=
//	GET  /api/sync/revision?item=&revision=   (supportsSync 2)
//	POST /api/sync/mutations   {"mutations": [...]}
//
// Each takes a `vault_id`, optional for the manifest only. A client sends the id its mirror was
// made from, and a server now serving another vault (a vault switch, or a
// store that started over) answers 409 VAULT_MISMATCH instead of letting the
// mirror sync into a vault it was never a copy of.

// syncProtocolVersion is what /api/capabilities advertises as supportsSync. A
// change a client must know about raises it.
//
//	1  manifest, changes and mutations (server 2.91.1)
//	2  adds revision reads, GET /api/sync/revision, which the engine uses for
//	   a conflict's base text so edits to different lines merge on their own
const syncProtocolVersion = 2

// How often the maintenance loop reconciles a vault with a store, and how
// long it waits after a watcher hint for the burst to finish.
const (
	syncMaintenanceInterval = 5 * time.Minute
	syncHintDebounce        = 2 * time.Second
)

type syncErrorBody struct {
	Code    string              `json:"code"`
	Message string              `json:"message"`
	Errors  map[string][]string `json:"errors,omitempty"`
}

func writeSyncError(w http.ResponseWriter, err error) {
	var validation *vault.SyncValidationError
	var maxBytes *http.MaxBytesError
	switch {
	case errors.As(err, &validation):
		writeJSON(w, http.StatusUnprocessableEntity, syncErrorBody{
			Code: "VALIDATION_FAILED", Message: validation.Error(), Errors: validation.Fields,
		})
	case errors.Is(err, vault.ErrSyncCursorExpired):
		writeJSON(w, http.StatusGone, syncErrorBody{
			Code:    "CURSOR_EXPIRED",
			Message: "This copy is older than the server's change history. It catches up from the full list instead.",
		})
	case errors.Is(err, vault.ErrSyncRevisionNotRetained):
		writeJSON(w, http.StatusNotFound, syncErrorBody{
			Code:    "REVISION_NOT_RETAINED",
			Message: "The server no longer keeps this revision.",
		})
	case errors.Is(err, errSyncVaultMismatch):
		writeJSON(w, http.StatusConflict, syncErrorBody{
			Code:    "VAULT_MISMATCH",
			Message: "The server now serves a different vault than this copy was made from.",
		})
	case errors.Is(err, vault.ErrSyncRetry):
		w.Header().Set("Retry-After", "2")
		writeJSON(w, http.StatusServiceUnavailable, syncErrorBody{Code: "SYNC_RETRY", Message: err.Error()})
	case errors.Is(err, vault.ErrSyncUnavailable):
		w.Header().Set("Retry-After", "30")
		writeJSON(w, http.StatusServiceUnavailable, syncErrorBody{
			Code: "SYNC_UNAVAILABLE", Message: "The server could not read or write its sync state. Try again shortly.",
		})
	case errors.As(err, &maxBytes):
		writeJSON(w, http.StatusRequestEntityTooLarge, syncErrorBody{
			Code: "REQUEST_TOO_LARGE", Message: "This request is larger than the server takes. Send fewer changes at once.",
		})
	default:
		var statusErr httpStatusError
		if errors.As(err, &statusErr) {
			writeJSON(w, statusErr.code, syncErrorBody{Code: errorCodeForStatus(statusErr.code), Message: statusErr.Error()})
			return
		}
		log.Printf("sync handler error: %v", err)
		writeJSON(w, http.StatusInternalServerError, syncErrorBody{Code: "internal_error", Message: "internal server error"})
	}
}

var errSyncVaultMismatch = errors.New("vault mismatch")

// syncVault is the vault a sync request is about, refused when the client
// names another one. The change feed and mutations must name it: a cursor or
// an item id means nothing in another vault, or in a store that started over.
// The manifest may leave it out, which is how a new copy learns the id.
func (s *Server) syncVault(r *http.Request, required bool) (*vault.Vault, error) {
	v := s.currentVault()
	want := strings.TrimSpace(r.URL.Query().Get("vault_id"))
	if want == "" {
		if required {
			return nil, httpStatusError{code: http.StatusBadRequest, msg: "vault_id is required: send the id from the manifest this copy was made from"}
		}
		return v, nil
	}
	have, err := v.SyncVaultID()
	if err != nil {
		return nil, err
	}
	if have != want {
		return nil, errSyncVaultMismatch
	}
	return v, nil
}

func queryInt(r *http.Request, names ...string) (int64, bool, error) {
	for _, name := range names {
		raw := strings.TrimSpace(r.URL.Query().Get(name))
		if raw == "" {
			continue
		}
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return 0, true, httpStatusError{code: http.StatusBadRequest, msg: name + " must be a whole number"}
		}
		return value, true, nil
	}
	return 0, false, nil
}

func queryBool(r *http.Request, names ...string) bool {
	for _, name := range names {
		switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get(name))) {
		case "1", "true", "yes":
			return true
		}
	}
	return false
}

func (s *Server) syncManifest(w http.ResponseWriter, r *http.Request) {
	v, err := s.syncVault(r, false)
	if err != nil {
		writeSyncError(w, err)
		return
	}
	page, _, err := queryInt(r, "page")
	if err != nil {
		writeSyncError(w, err)
		return
	}
	// perPage is this server's spelling; per_page is Cloud's.
	perPage, _, err := queryInt(r, "perPage", "per_page")
	if err != nil {
		writeSyncError(w, err)
		return
	}
	out, err := v.SyncManifest(int(page), int(perPage), queryBool(r, "include_content", "includeContent"))
	if err != nil {
		writeSyncError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) syncChanges(w http.ResponseWriter, r *http.Request) {
	v, err := s.syncVault(r, true)
	if err != nil {
		writeSyncError(w, err)
		return
	}
	after, _, err := queryInt(r, "after")
	if err != nil {
		writeSyncError(w, err)
		return
	}
	limit, _, err := queryInt(r, "limit")
	if err != nil {
		writeSyncError(w, err)
		return
	}
	out, err := v.SyncChanges(after, int(limit))
	if err != nil {
		writeSyncError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// syncRevision answers one revision of an item as {"data": CloudSyncRevision}.
// A revision the store no longer has is 404 REVISION_NOT_RETAINED, never a
// 500: the engine takes any error as "no base" and keeps the conflict two-way.
func (s *Server) syncRevision(w http.ResponseWriter, r *http.Request) {
	v, err := s.syncVault(r, true)
	if err != nil {
		writeSyncError(w, err)
		return
	}
	// item is this server's spelling; item_id is the field's own name.
	itemID := strings.TrimSpace(r.URL.Query().Get("item"))
	if itemID == "" {
		itemID = strings.TrimSpace(r.URL.Query().Get("item_id"))
	}
	if itemID == "" {
		writeSyncError(w, httpStatusError{code: http.StatusBadRequest, msg: "item is required"})
		return
	}
	revision, ok, err := queryInt(r, "revision")
	if err != nil {
		writeSyncError(w, err)
		return
	}
	if !ok || revision < 1 {
		writeSyncError(w, httpStatusError{code: http.StatusBadRequest, msg: "revision must be a positive whole number"})
		return
	}
	out, err := v.SyncReadRevision(itemID, revision)
	if err != nil {
		writeSyncError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Data vault.SyncRevision `json:"data"`
	}{out})
}

func (s *Server) syncMutations(w http.ResponseWriter, r *http.Request) {
	v, err := s.syncVault(r, true)
	if err != nil {
		writeSyncError(w, err)
		return
	}
	cfg := s.currentConfig()
	maxNote := cfg.MaxNoteBytes
	if maxNote <= 0 {
		maxNote = 10 << 20
	}
	// The engine batches 100 mutations whatever their size, so a cap near one
	// note would refuse ordinary batches of drawings. JSON escaping can double
	// a body. A batch past this is refused whole (413); a client sends one of
	// that size in smaller requests.
	r.Body = http.MaxBytesReader(w, r.Body, max(256<<20, 4*maxNote)+jsonEnvelopeBytes)
	var req struct {
		Mutations []vault.SyncMutation `json:"mutations"`
	}
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		writeSyncError(w, httpStatusError{code: http.StatusUnsupportedMediaType, msg: "request body must be JSON sent as Content-Type: application/json"})
		return
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxBytes *http.MaxBytesError
		if errors.As(err, &maxBytes) {
			writeSyncError(w, err)
			return
		}
		writeSyncError(w, httpStatusError{code: http.StatusBadRequest, msg: err.Error()})
		return
	}
	out, err := v.SyncMutate(req.Mutations)
	if err != nil {
		writeSyncError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// --- Maintenance ---

// StartSyncMaintenance keeps the sync store of the vault being served in step
// with edits made outside the server: a reconcile at once, on a watcher hint
// (debounced) and every few minutes, plus the daily prune the reconcile runs.
// A vault without a store costs a nil check per tick. It follows vault
// switches and stops with ctx.
//
// The loop's own state is guarded by syncMaintMu, never by s.mu: stopping it
// waits for a reconcile in progress, and every request takes s.mu to find the
// vault, so waiting under s.mu would stall them all.
func (s *Server) StartSyncMaintenance(ctx context.Context) {
	s.syncMaintMu.Lock()
	defer s.syncMaintMu.Unlock()
	if s.syncCancel != nil {
		return
	}
	loopCtx, cancel := context.WithCancel(ctx)
	s.syncCtx, s.syncCancel = loopCtx, cancel
	s.runSyncMaintenanceLocked(loopCtx, s.currentVault(), s.currentWatcher())
}

// StopSyncMaintenance stops the loop and waits for a reconcile in progress,
// so a caller can remove the vault afterwards (Windows refuses to delete a
// directory a reconcile is reading).
func (s *Server) StopSyncMaintenance() {
	s.syncMaintMu.Lock()
	defer s.syncMaintMu.Unlock()
	if s.syncVaultCancel != nil {
		s.syncVaultCancel()
	}
	if s.syncCancel != nil {
		s.syncCancel()
	}
	s.syncCancel, s.syncVaultCancel = nil, nil
}

// restartSyncMaintenance moves the loop to the vault a switch just selected.
// Two switches at once take turns, so neither leaves a loop behind.
func (s *Server) restartSyncMaintenance(v *vault.Vault, w *watcher.Watcher) {
	s.syncMaintMu.Lock()
	defer s.syncMaintMu.Unlock()
	if s.syncCancel == nil {
		return
	}
	if s.syncVaultCancel != nil {
		s.syncVaultCancel()
	}
	s.runSyncMaintenanceLocked(s.syncCtx, v, w)
}

func (s *Server) runSyncMaintenanceLocked(parent context.Context, v *vault.Vault, w *watcher.Watcher) {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	s.syncVaultCancel = func() {
		cancel()
		<-done
	}
	var hints <-chan vault.ChangeEvent
	unsubscribe := func() {}
	if w != nil && w.Active() {
		hints, unsubscribe = w.Subscribe()
	}
	go func() {
		defer close(done)
		defer unsubscribe()
		reconcile := func() {
			if !v.SyncActive() {
				return
			}
			if err := v.SyncReconcile(); err != nil {
				log.Printf("sync: reconcile: %v", err)
			}
		}
		reconcile()
		ticker := time.NewTicker(syncMaintenanceInterval)
		defer ticker.Stop()
		var debounce <-chan time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				reconcile()
			case _, ok := <-hints:
				if !ok {
					hints = nil
					continue
				}
				if debounce == nil {
					debounce = time.After(syncHintDebounce)
				}
			case <-debounce:
				debounce = nil
				reconcile()
			}
		}
	}()
}
