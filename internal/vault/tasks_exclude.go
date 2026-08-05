package vault

import "strings"

// The vault-level "exclude this folder from Tasks" list (#458). Byte-for-byte
// mirror of packages/shared-domain/src/tasks-excluded-folders.ts: change both
// together. Entries are vault-relative directory paths exactly as they exist
// on disk, so remapped system folders (#115) need no translation.

// normalizeTasksExcludedFolder validates one entry: forward slashes, no empty
// or dot segments, no traversal. Returns "" when invalid.
func normalizeTasksExcludedFolder(value string) string {
	parts := []string{}
	for _, seg := range strings.Split(strings.ReplaceAll(value, "\\", "/"), "/") {
		s := strings.TrimSpace(seg)
		if s == "" {
			continue
		}
		if s == "." || s == ".." {
			return ""
		}
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return ""
	}
	joined := strings.Join(parts, "/")
	if len(joined) > 512 {
		return ""
	}
	return joined
}

// normalizeTasksExcludedFolders drops invalid entries and duplicates,
// preserving order.
func normalizeTasksExcludedFolders(values []string) []string {
	out := []string{}
	seen := map[string]struct{}{}
	for _, entry := range values {
		cleaned := normalizeTasksExcludedFolder(entry)
		if cleaned == "" {
			continue
		}
		if _, dup := seen[cleaned]; dup {
			continue
		}
		seen[cleaned] = struct{}{}
		out = append(out, cleaned)
	}
	return out
}

// normalizeTasksSettings carries the Tasks-system settings through the
// settings round-trip: a validated exclusion list, or nil so vault.json stays
// free of empty stubs.
func normalizeTasksSettings(value *TasksSettings) *TasksSettings {
	if value == nil {
		return nil
	}
	excluded := normalizeTasksExcludedFolders(value.ExcludedFolders)
	if len(excluded) == 0 {
		return nil
	}
	return &TasksSettings{ExcludedFolders: excluded}
}

// tasksExcludedFolders reads the exclusion list off already-normalized
// settings.
func tasksExcludedFolders(settings VaultSettings) []string {
	if settings.Tasks == nil {
		return nil
	}
	return settings.Tasks.ExcludedFolders
}

// isPathExcludedFromTasks reports whether a vault-relative POSIX path lives
// inside any excluded folder. Segment-prefix match, case-sensitive like the
// rest of the vault layer: `inbox/Books` excludes `inbox/Books/x.md` and
// `inbox/Books/sub/y.md`, never `inbox/Bookshelf.md`.
func isPathExcludedFromTasks(relPath string, excluded []string) bool {
	for _, folder := range excluded {
		if relPath == folder || strings.HasPrefix(relPath, folder+"/") {
			return true
		}
	}
	return false
}
