package vault

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Asset deletion mirrors the desktop implementation (vault.ts) exactly:
// a delete moves the file into .zennotes/deleted-assets/<undo-token>/ next to
// a .zn-deleted.json holding the original location, so the Trash view can
// list and restore it even across restarts and across the two
// implementations. Both apps read each other's stores; the on-disk layout is
// a contract, not an implementation detail.
const (
	deletedAssetsDir     = "deleted-assets"
	deletedAssetMetaFile = ".zn-deleted.json"
)

// Matches the desktop's token validation: 36 chars of hex and dashes.
var deletedAssetTokenRe = regexp.MustCompile(`^[0-9a-fA-F-]{36}$`)

func newUndoToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func cleanDeletedAssetToken(token string) (string, error) {
	if !deletedAssetTokenRe.MatchString(token) {
		return "", errors.New("deleted asset restore token is invalid")
	}
	return token, nil
}

func cleanDeletedAssetPath(rel string) (string, error) {
	normalized := strings.Trim(strings.TrimSpace(filepath.ToSlash(rel)), "/")
	if normalized == "" {
		return "", errors.New("deleted asset path is required")
	}
	for _, part := range strings.Split(normalized, "/") {
		if part == internalVaultDir {
			return "", errors.New("cannot restore internal ZenNotes files")
		}
	}
	if strings.EqualFold(filepath.Ext(normalized), ".md") {
		return "", errors.New("use note actions to restore markdown notes")
	}
	return normalized, nil
}

func (v *Vault) deletedAssetsRoot() string {
	return filepath.Join(v.root, internalVaultDir, deletedAssetsDir)
}

// DuplicateAsset copies an asset next to itself as "<name> copy<ext>",
// mirroring the desktop duplicateAsset.
func (v *Vault) DuplicateAsset(rel string) (AssetMeta, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	srcAbs, err := v.assertAssetFile(rel)
	if err != nil {
		return AssetMeta{}, err
	}
	ext := filepath.Ext(srcAbs)
	stem := strings.TrimSuffix(filepath.Base(srcAbs), ext)
	destAbs := uniquePath(filepath.Dir(srcAbs), stem+" copy", ext)
	if err := copyFile(srcAbs, destAbs, v.fileMode); err != nil {
		return AssetMeta{}, err
	}
	return v.assetMetaForAbs(destAbs)
}

// DeleteAsset moves an asset into the deleted-assets store and returns the
// restore handle, mirroring the desktop deleteAsset.
func (v *Vault) DeleteAsset(rel string) (DeletedAsset, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	srcAbs, err := v.assertAssetFile(rel)
	if err != nil {
		return DeletedAsset{}, err
	}
	srcRel, err := filepath.Rel(v.root, srcAbs)
	if err != nil {
		return DeletedAsset{}, err
	}
	undoToken, err := newUndoToken()
	if err != nil {
		return DeletedAsset{}, err
	}
	trashDir := filepath.Join(v.deletedAssetsRoot(), undoToken)
	if err := os.MkdirAll(trashDir, v.dirMode); err != nil {
		return DeletedAsset{}, err
	}
	name := filepath.Base(srcAbs)
	deletedAt := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	deleted := DeletedAsset{
		Path:      filepath.ToSlash(srcRel),
		Name:      name,
		UndoToken: undoToken,
		DeletedAt: deletedAt,
	}
	meta, err := json.MarshalIndent(map[string]string{
		"path":      deleted.Path,
		"name":      deleted.Name,
		"deletedAt": deleted.DeletedAt,
	}, "", "  ")
	if err != nil {
		return DeletedAsset{}, err
	}
	// Metadata first, file move last: a failure anywhere leaves the asset
	// still in the vault. The old order (rename, then metadata) could hit a
	// write error (disk full, permissions) after the move and strand the
	// asset in a token dir the Trash view skips, gone from the vault with no
	// in-app way back.
	if err := os.WriteFile(filepath.Join(trashDir, deletedAssetMetaFile), meta, v.fileMode); err != nil {
		_ = os.RemoveAll(trashDir)
		return DeletedAsset{}, err
	}
	if err := os.Rename(srcAbs, filepath.Join(trashDir, name)); err != nil {
		_ = os.RemoveAll(trashDir)
		return DeletedAsset{}, err
	}
	return deleted, nil
}

// ListDeletedAssets enumerates restorable entries in the deleted-assets
// store, newest first. Entries without metadata are skipped, exactly like the
// desktop (pre-2.11 deletes have none).
func (v *Vault) ListDeletedAssets() ([]DeletedAsset, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	entries, err := os.ReadDir(v.deletedAssetsRoot())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []DeletedAsset{}, nil
		}
		return nil, err
	}
	out := []DeletedAsset{}
	for _, entry := range entries {
		undoToken := entry.Name()
		raw, err := os.ReadFile(filepath.Join(v.deletedAssetsRoot(), undoToken, deletedAssetMetaFile))
		if err != nil {
			continue
		}
		var meta struct {
			Path      string `json:"path"`
			Name      string `json:"name"`
			DeletedAt string `json:"deletedAt"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil || meta.Path == "" || meta.Name == "" {
			continue
		}
		// The asset file itself must still be present to be restorable.
		if _, err := os.Stat(filepath.Join(v.deletedAssetsRoot(), undoToken, meta.Name)); err != nil {
			continue
		}
		out = append(out, DeletedAsset{
			Path:      meta.Path,
			Name:      meta.Name,
			UndoToken: undoToken,
			DeletedAt: meta.DeletedAt,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].DeletedAt > out[j].DeletedAt
	})
	return out, nil
}

// RestoreDeletedAsset moves an asset back to its original folder, deduping
// the filename if something new took its place, mirroring the desktop.
// Only the token comes from the caller; the stored .zn-deleted.json decides
// what gets restored and where. Trusting a client-supplied name here once
// let a request naming the metadata file itself "restore" that file and
// then destroy the real asset bytes with the trash dir cleanup.
func (v *Vault) RestoreDeletedAsset(deleted DeletedAsset) (AssetMeta, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	undoToken, err := cleanDeletedAssetToken(deleted.UndoToken)
	if err != nil {
		return AssetMeta{}, err
	}
	trashDir := filepath.Join(v.deletedAssetsRoot(), undoToken)
	raw, err := os.ReadFile(filepath.Join(trashDir, deletedAssetMetaFile))
	if err != nil {
		return AssetMeta{}, errors.New("deleted asset entry not found")
	}
	var stored struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return AssetMeta{}, errors.New("deleted asset entry is unreadable")
	}
	targetRel, err := cleanDeletedAssetPath(stored.Path)
	if err != nil {
		return AssetMeta{}, err
	}
	name, err := cleanAssetFilename(stored.Name)
	if err != nil {
		return AssetMeta{}, err
	}
	if name == deletedAssetMetaFile {
		return AssetMeta{}, errors.New("deleted asset entry is unreadable")
	}
	srcAbs := filepath.Join(trashDir, name)
	targetAbs, err := SafeJoin(v.root, targetRel)
	if err != nil {
		return AssetMeta{}, err
	}
	targetDir := filepath.Dir(targetAbs)
	if err := os.MkdirAll(targetDir, v.dirMode); err != nil {
		return AssetMeta{}, err
	}
	base := filepath.Base(targetAbs)
	ext := filepath.Ext(base)
	finalAbs := uniquePath(targetDir, strings.TrimSuffix(base, ext), ext)
	if err := os.Rename(srcAbs, finalAbs); err != nil {
		return AssetMeta{}, err
	}
	if err := os.RemoveAll(trashDir); err != nil {
		return AssetMeta{}, err
	}
	return v.assetMetaForAbs(finalAbs)
}

// PurgeDeletedAsset permanently deletes one entry from the store.
func (v *Vault) PurgeDeletedAsset(undoToken string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	token, err := cleanDeletedAssetToken(undoToken)
	if err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(v.deletedAssetsRoot(), token))
}

// EmptyDeletedAssets permanently deletes every entry in the store.
func (v *Vault) EmptyDeletedAssets() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return os.RemoveAll(v.deletedAssetsRoot())
}
