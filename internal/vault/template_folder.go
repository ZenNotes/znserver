package vault

import (
	"encoding/json"
	"strings"
	"unicode/utf16"
)

// The vault's template folder (#1024, ZenNotes 2.80): a folder of ordinary
// notes whose every note is a template, the way Obsidian's core Templates
// plugin reads one. Stored in vault.json as `templates.folder`, a
// vault-relative directory path exactly as it exists on disk (the shape of the
// Tasks folder lists), so remapped system folders (#115) need no translation.
// Byte-for-byte mirror of packages/shared-domain/src/template-folder.ts in the
// desktop repo: change both together.
//
// The clients read the templates themselves, through the note routes every
// server already has, so this side only has to do three things: keep the
// setting through a settings round trip (#446/#379), leave the folder out of
// the task scan (a template's `- [ ]` lines are placeholders, not work), and
// leave it out of text search (nobody searching their notes wants one).

// maxTemplateFolderLength caps the folder path, counted in UTF-16 code units
// as the TypeScript side's `String.length` counts it.
const maxTemplateFolderLength = 512

// normalizeTemplateFolder validates a configured folder: backslashes become
// slashes, empty segments drop, each segment loses the whitespace JavaScript's
// trim() removes, and any segment starting with a dot (`.`, `..`, `.zennotes`,
// any hidden directory) refuses the whole value. Returns "" when unusable.
func normalizeTemplateFolder(value string) string {
	parts := []string{}
	for _, seg := range strings.Split(strings.ReplaceAll(value, "\\", "/"), "/") {
		s := strings.TrimFunc(seg, isJSWhitespace)
		if s == "" {
			continue
		}
		if strings.HasPrefix(s, ".") {
			return ""
		}
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return ""
	}
	joined := strings.Join(parts, "/")
	if len(utf16.Encode([]rune(joined))) > maxTemplateFolderLength {
		return ""
	}
	return joined
}

// normalizeTemplateFolderSettings carries `templates` through the settings
// round trip: the cleaned folder, or nil when none is usable, so an untouched
// vault.json never grows an empty stub (normalizeTasksSettings' rule).
func normalizeTemplateFolderSettings(value *TemplateFolderSettings) *TemplateFolderSettings {
	if value == nil {
		return nil
	}
	folder := normalizeTemplateFolder(value.Folder)
	if folder == "" {
		return nil
	}
	return &TemplateFolderSettings{Folder: folder}
}

// resolveTemplateFolder is the folder in effect for already-parsed settings,
// or "" when the vault has none.
func resolveTemplateFolder(settings VaultSettings) string {
	if settings.Templates == nil {
		return ""
	}
	return normalizeTemplateFolder(settings.Templates.Folder)
}

// isPathInTemplateFolder reports whether a vault-relative POSIX path is inside
// the folder: a whole-segment prefix match, case-sensitive like the rest of
// the vault layer, so `Templates` never holds `Templates Old/a.md`. No folder
// matches nothing.
func isPathInTemplateFolder(relPath, folder string) bool {
	if folder == "" {
		return false
	}
	return relPath == folder || strings.HasPrefix(relPath, folder+"/")
}

// rewriteTemplateFolderForRename follows a folder rename or move: a template
// folder named by the renamed folder, or inside it, moves with it. Anything
// else comes back unchanged (the same pointer).
func rewriteTemplateFolderForRename(templates *TemplateFolderSettings, oldRel, newRel string) *TemplateFolderSettings {
	if templates == nil || templates.Folder == "" || oldRel == "" || newRel == "" || oldRel == newRel {
		return templates
	}
	folder := templates.Folder
	if folder != oldRel && !strings.HasPrefix(folder, oldRel+"/") {
		return templates
	}
	moved := normalizeTemplateFolder(newRel + folder[len(oldRel):])
	if moved == "" {
		return templates
	}
	return &TemplateFolderSettings{Folder: moved}
}

// tasksFolderLists is the pair every task scan applies: the excluded folders
// and the Task folders off already-normalized settings, with the template
// folder added to the excluded ones, so its placeholders stay out of Tasks
// even inside a Task folder. Mirrors tasksFolderListsForSettings in
// tasks-excluded-folders.ts. The returned slice is a fresh copy.
func tasksFolderLists(settings VaultSettings) (excluded, included []string) {
	excluded = append([]string{}, tasksExcludedFolders(settings)...)
	included = tasksIncludedFolders(settings)
	if folder := resolveTemplateFolder(settings); folder != "" {
		for _, entry := range excluded {
			if entry == folder {
				return excluded, included
			}
		}
		excluded = append(excluded, folder)
	}
	return excluded, included
}

// UnmarshalJSON reads `templates` the way the TypeScript normalizer reads an
// `unknown`: a value that is not an object, or a folder that is not a string,
// counts as absent instead of failing the whole vault.json (and every request
// that reads settings with it). Keys match exactly, as a JavaScript property
// read does, not case-insensitively as Go's struct decoding does.
func (s *TemplateFolderSettings) UnmarshalJSON(data []byte) error {
	*s = TemplateFolderSettings{}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return nil
	}
	var folder string
	if json.Unmarshal(fields["folder"], &folder) == nil {
		s.Folder = folder
	}
	return nil
}
