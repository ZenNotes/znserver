package vault

import "strings"

// The vault-level folder lists for Tasks: the excluded folders (#458) and the
// Task folders that narrow Tasks to themselves (#962). Byte-for-byte mirror of
// packages/shared-domain/src/tasks-excluded-folders.ts: change both together.
// Entries are vault-relative directory paths exactly as they exist on disk, so
// remapped system folders (#115) need no translation.

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
// settings round-trip: validated excluded and Task folder lists, each left out
// when empty, or nil so vault.json stays free of empty stubs. The Task folders
// are validated exactly like the excluded ones.
func normalizeTasksSettings(value *TasksSettings) *TasksSettings {
	if value == nil {
		return nil
	}
	excluded := normalizeTasksExcludedFolders(value.ExcludedFolders)
	included := normalizeTasksExcludedFolders(value.IncludedFolders)
	if len(excluded) == 0 && len(included) == 0 {
		return nil
	}
	out := &TasksSettings{}
	if len(excluded) > 0 {
		out.ExcludedFolders = excluded
	}
	if len(included) > 0 {
		out.IncludedFolders = included
	}
	return out
}

// tasksExcludedFolders reads the exclusion list off already-normalized
// settings.
func tasksExcludedFolders(settings VaultSettings) []string {
	if settings.Tasks == nil {
		return nil
	}
	return settings.Tasks.ExcludedFolders
}

// tasksIncludedFolders reads the Task folders off already-normalized settings.
func tasksIncludedFolders(settings VaultSettings) []string {
	if settings.Tasks == nil {
		return nil
	}
	return settings.Tasks.IncludedFolders
}

// isPathExcludedFromTasks reports whether a vault-relative POSIX path is kept
// out of Tasks by the folder lists. Segment-prefix match, case-sensitive like
// the rest of the vault layer: `inbox/Books` excludes `inbox/Books/x.md` and
// `inbox/Books/sub/y.md`, never `inbox/Bookshelf.md`. Once included names
// folders, a path outside all of them is out as well; excluded still applies
// inside them, so `Projects` with `Projects/Archive` excluded leaves the
// archive out.
func isPathExcludedFromTasks(relPath string, excluded, included []string) bool {
	if len(included) > 0 && !isInsideAnyFolder(relPath, included) {
		return true
	}
	return isInsideAnyFolder(relPath, excluded)
}

func isInsideAnyFolder(relPath string, folders []string) bool {
	for _, folder := range folders {
		if relPath == folder || strings.HasPrefix(relPath, folder+"/") {
			return true
		}
	}
	return false
}

// rewriteFolderList repoints one list's entries after a folder rename or move:
// an entry naming the folder, or a folder inside it, follows it.
func rewriteFolderList(list []string, oldRel, newRel string) []string {
	out := make([]string, 0, len(list))
	for _, entry := range list {
		if entry == oldRel || strings.HasPrefix(entry, oldRel+"/") {
			entry = newRel + entry[len(oldRel):]
		}
		out = append(out, entry)
	}
	return normalizeTasksExcludedFolders(out)
}

// rewriteTasksFoldersForRename keeps both folder lists naming the same folders
// after a rename or move. A renamed Task folder (#962) left behind would match
// nothing, and once it was the only one, Tasks would go empty. Mirrors
// rewriteTasksFoldersForRename in tasks-excluded-folders.ts.
func rewriteTasksFoldersForRename(tasks *TasksSettings, oldRel, newRel string) *TasksSettings {
	if tasks == nil || oldRel == "" || newRel == "" || oldRel == newRel {
		return tasks
	}
	next := &TasksSettings{}
	if len(tasks.ExcludedFolders) > 0 {
		next.ExcludedFolders = rewriteFolderList(tasks.ExcludedFolders, oldRel, newRel)
	}
	if len(tasks.IncludedFolders) > 0 {
		next.IncludedFolders = rewriteFolderList(tasks.IncludedFolders, oldRel, newRel)
	}
	return next
}
