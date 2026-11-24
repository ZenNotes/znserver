package vault

import (
	"errors"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Which files the sync protocol carries, and how it names them. The rules are
// the Cloud sync engine's, ported so that a mirror kept by that engine and this
// server agree on every path:
//
//   - normalizeSyncPath is normalizeCloudSyncPath
//     (packages/shared-domain/src/cloud-sync.ts in ZenNotes/zennotes).
//   - syncPathKey is cloudSyncPathKey: two paths with one key are one file on
//     a case-insensitive disk, so the server never lists both.
//   - shouldSyncPath is shouldSyncVaultPath, including its allow list for
//     `.zennotes/`, which is also what keeps this store (.zennotes/sync/) out.
//   - syncTextExtensions and syncMediaTypes are the desktop repository's
//     TEXT_EXTENSIONS and MEDIA_TYPES (apps/desktop/src/main/cloud-sync-filesystem.ts),
//     so an item's kind and media type are the same on both sides.
//
// Phase 2 of the offline copy (#1108) syncs text only: notes, the synced
// `.zennotes/` files and any other text file the engine would call text.
// Binary files (attachments) are phase 4 and are neither listed nor accepted.

var (
	syncWindowsReservedChars = regexp.MustCompile(`[:*?"<>|]`)
	syncControlChars         = regexp.MustCompile("[\x00-\x1f\x7f]")
	syncDriveLetter          = regexp.MustCompile(`^[A-Za-z]:/`)
)

var syncLocalOnlyDirs = map[string]struct{}{
	".git": {}, ".hg": {}, ".svn": {}, "node_modules": {}, "zennotes-cloud-sync": {},
}

var syncTextExtensions = map[string]struct{}{
	".base": {}, ".css": {}, ".csv": {}, ".excalidraw": {}, ".htm": {}, ".html": {},
	".ini": {}, ".js": {}, ".json": {}, ".jsx": {}, ".md": {}, ".mdx": {}, ".svg": {},
	".toml": {}, ".ts": {}, ".tsx": {}, ".txt": {}, ".xml": {}, ".yaml": {}, ".yml": {},
}

var syncMediaTypes = map[string]string{
	".css": "text/css", ".csv": "text/csv", ".gif": "image/gif", ".htm": "text/html",
	".html": "text/html", ".jpeg": "image/jpeg", ".jpg": "image/jpeg", ".js": "text/javascript",
	".json": "application/json", ".md": "text/markdown", ".pdf": "application/pdf",
	".png": "image/png", ".svg": "image/svg+xml", ".toml": "application/toml",
	".txt": "text/plain", ".webp": "image/webp", ".xml": "application/xml",
	".yaml": "application/yaml", ".yml": "application/yaml",
}

// syncStoreRelDir is where the protocol keeps its own state. shouldSyncPath
// already refuses it (an unknown `.zennotes/` file is local-only), and the
// watcher never watches it.
const syncStoreRelDir = internalVaultDir + "/sync"

var errInvalidSyncPath = errors.New("invalid sync path")

// normalizeSyncPath returns the portable, vault-relative spelling the wire
// protocol uses, or errInvalidSyncPath for one that is not valid on every
// platform ZenNotes runs on.
func normalizeSyncPath(p string) (string, error) {
	normalized := norm.NFC.String(strings.ReplaceAll(p, `\`, "/"))
	if normalized == "" ||
		strings.HasPrefix(normalized, "/") ||
		strings.HasSuffix(normalized, "/") ||
		syncDriveLetter.MatchString(normalized) ||
		syncControlChars.MatchString(normalized) {
		return "", errInvalidSyncPath
	}
	for _, segment := range strings.Split(normalized, "/") {
		if segment == "" || segment == "." || segment == ".." || syncWindowsReservedChars.MatchString(segment) {
			return "", errInvalidSyncPath
		}
	}
	return normalized, nil
}

// syncPathKey is the case-folded collision key of a path.
func syncPathKey(p string) string {
	normalized, err := normalizeSyncPath(p)
	if err != nil {
		return strings.ToLower(p)
	}
	return strings.ToLower(normalized)
}

// shouldSyncPath reports whether a vault-relative path is one sync carries at
// all, before its contents are looked at.
func shouldSyncPath(p string) bool {
	normalized, err := normalizeSyncPath(p)
	if err != nil {
		return false
	}
	lower := strings.ToLower(normalized)
	segments := strings.Split(lower, "/")
	name := segments[len(segments)-1]
	for _, segment := range segments {
		if _, ok := syncLocalOnlyDirs[segment]; ok {
			return false
		}
	}
	if name == ".ds_store" || name == "thumbs.db" ||
		strings.HasSuffix(name, ".tmp") || strings.HasSuffix(name, ".bak") || strings.HasSuffix(name, "~") {
		return false
	}
	if !strings.HasPrefix(lower, internalVaultDir+"/") {
		return true
	}
	return lower == internalVaultDir+"/vault.json" ||
		strings.HasPrefix(lower, internalVaultDir+"/comments/") ||
		(strings.HasPrefix(lower, internalVaultDir+"/agent-edits/") && strings.HasSuffix(lower, ".json")) ||
		(strings.HasPrefix(lower, internalVaultDir+"/note-metadata/") && strings.HasSuffix(lower, ".metadata.json")) ||
		strings.HasPrefix(lower, internalVaultDir+"/templates/") ||
		strings.HasPrefix(lower, internalVaultDir+"/workflows/")
}

// shouldTraverseSyncDir is shouldTraverseCloudSyncDirectory: skip device-local
// trees before reading them. The store's own directory is skipped too.
func shouldTraverseSyncDir(rel string) bool {
	if rel == "" {
		return true
	}
	normalized, err := normalizeSyncPath(rel)
	if err != nil {
		return false
	}
	lower := strings.ToLower(normalized)
	if lower == syncStoreRelDir || strings.HasPrefix(lower, syncStoreRelDir+"/") {
		return false
	}
	for _, segment := range strings.Split(lower, "/") {
		if _, ok := syncLocalOnlyDirs[segment]; ok {
			return false
		}
	}
	return true
}

// syncTextKind reports whether bytes at this path are a text item, by the
// desktop repository's rule: a text extension and valid UTF-8.
func syncTextKind(rel string, data []byte) bool {
	if _, ok := syncTextExtensions[strings.ToLower(path.Ext(rel))]; !ok {
		return false
	}
	return utf8.Valid(data)
}

// syncHasTextExtension is the half of syncTextKind a path alone can answer.
func syncHasTextExtension(rel string) bool {
	_, ok := syncTextExtensions[strings.ToLower(path.Ext(rel))]
	return ok
}

// syncMediaType is the desktop repository's mediaType for a text item.
func syncMediaType(rel string) string {
	if media, ok := syncMediaTypes[strings.ToLower(path.Ext(rel))]; ok {
		return media
	}
	return "text/plain"
}
