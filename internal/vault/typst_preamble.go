package vault

import "strings"

// The vault-level Typst preamble folder (#486, configurable since #562).
// Byte-for-byte mirror of packages/shared-domain/src/typst-preamble-folder.ts:
// change both together. The setting names a single directory NAME matched at
// any depth, so `inbox/typst/physics.md` and `archive/notes/typst/maths.md`
// are both preambles.
//
// Preamble notes hold Typst source, not prose: `#let vec(x) = bold(x)` and the
// `#var` references inside formulas are variables, and indexing them filled a
// vault's tag list with `let` and every variable name. Tags are the only thing
// skipped; a preamble keeps its excerpt, wikilinks and searchability.

// DefaultTypstPreambleFolder is the folder name used when the vault says
// nothing.
const DefaultTypstPreambleFolder = "typst"

const maxTypstPreambleFolderLength = 128

// Same character rules as a system folder path (#115): one directory name, no
// separators, nothing that needs escaping on any platform we ship to.
const invalidTypstPreambleFolderChars = `\/:*?"<>|#^[]`

// normalizeTypstPreambleFolder validates a configured folder name: exactly one
// directory segment, no traversal, no dotfiles. Returns "" when the value is
// unusable and the caller should fall back to the default.
func normalizeTypstPreambleFolder(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || len(trimmed) > maxTypstPreambleFolderLength {
		return ""
	}
	if trimmed == "." || trimmed == ".." || strings.HasPrefix(trimmed, ".") {
		return ""
	}
	if strings.ContainsAny(trimmed, invalidTypstPreambleFolderChars) {
		return ""
	}
	return trimmed
}

// resolveTypstPreambleFolder returns the folder name in effect for the given
// already-parsed settings. Never fails: anything malformed resolves to the
// default.
func resolveTypstPreambleFolder(settings VaultSettings) string {
	if settings.TypstPreambles == nil {
		return DefaultTypstPreambleFolder
	}
	if folder := normalizeTypstPreambleFolder(settings.TypstPreambles.Folder); folder != "" {
		return folder
	}
	return DefaultTypstPreambleFolder
}

// normalizeTypstPreambleSettings carries the preamble settings through the
// settings round-trip. Returns nil for the default folder so an untouched
// vault.json never grows an empty stub, matching normalizeTasksSettings.
func normalizeTypstPreambleSettings(value *TypstPreambleSettings) *TypstPreambleSettings {
	if value == nil {
		return nil
	}
	folder := normalizeTypstPreambleFolder(value.Folder)
	if folder == "" || folder == DefaultTypstPreambleFolder {
		return nil
	}
	return &TypstPreambleSettings{Folder: folder}
}

// isTypstPreamblePath reports whether a vault-relative POSIX path is a
// preamble note, i.e. sits in a directory with the configured name at any
// depth. Case-insensitive, matching how the rest of the preamble layer treats
// tags and titles.
func isTypstPreamblePath(relPath, folder string) bool {
	name := strings.ToLower(folder)
	if name == "" {
		return false
	}
	parts := strings.Split(relPath, "/")
	if len(parts) < 2 {
		return false
	}
	// The last part is the file itself, so look for the folder among the parents.
	for _, part := range parts[:len(parts)-1] {
		if strings.ToLower(strings.TrimSpace(part)) == name {
			return true
		}
	}
	return false
}
