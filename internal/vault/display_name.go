package vault

import (
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A vault's display name (#692): what the clients call the vault, kept in
// `.zennotes/vault.json` as `displayName`, distinct from the folder's name on
// disk. Byte-for-byte mirror of packages/shared-domain/src/vault-display-name.ts:
// change both together. A name that survives one runtime's round-trip must
// survive the other's, or a desktop-written name is lost on the web client's
// next settings save (the #446/#379 round-trip rule).
//
// Absent means the folder name, so a vault that never set one behaves exactly
// as before, and clearing the field is the same as never having set it.

// maxVaultDisplayNameLength is longer than any name that fits a sidebar
// header; a limit rather than a layout rule, so a pasted paragraph cannot
// become the vault's name. Counted in UTF-16 code units, as the TypeScript
// side's `String.length` does, so the two runtimes cut at the same place.
const maxVaultDisplayNameLength = 64

// normalizeVaultDisplayName is the name as it is stored and shown, in this
// order: C0 and C1 control characters other than the whitespace ones dropped,
// runs of whitespace (tabs, newlines and the Unicode separators included)
// collapsed to one space, trimmed, cut at the limit without splitting a
// surrogate pair. Empty when nothing usable is left, so vault.json is written
// without the key (`omitempty`) and every reader's folder-name fallback holds.
func normalizeVaultDisplayName(raw string) string {
	if !utf8.ValidString(raw) {
		raw = strings.ToValidUTF8(raw, "")
	}
	var b strings.Builder
	pendingSpace := false
	for _, r := range raw {
		switch {
		case isJSWhitespace(r):
			pendingSpace = true
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			// Dropped. Doing this inside the whitespace pass is the same as
			// dropping first: a control between two spaces leaves one space.
		default:
			if pendingSpace && b.Len() > 0 {
				b.WriteByte(' ')
			}
			pendingSpace = false
			b.WriteRune(r)
		}
	}
	cleaned := b.String()
	if cleaned == "" {
		return ""
	}
	return truncateUTF16(cleaned, maxVaultDisplayNameLength)
}

// isJSWhitespace matches JavaScript's `\s`: the ASCII whitespace controls,
// the Unicode space separators, the line and paragraph separators, and the
// BOM, which Go's unicode.IsSpace does not count.
func isJSWhitespace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x2028, 0x2029, 0xfeff:
		return true
	}
	return unicode.Is(unicode.Zs, r)
}

// truncateUTF16 cuts s after at most limit UTF-16 code units, never inside a
// surrogate pair, and trims the space a cut can leave at the end. Only ' '
// can be there: the whitespace pass left no other.
func truncateUTF16(s string, limit int) string {
	units := 0
	for i, r := range s {
		width := 1
		if r > 0xffff {
			width = 2
		}
		if units+width > limit {
			return strings.TrimRight(s[:i], " ")
		}
		units += width
	}
	return s
}

// resolveVaultName is the name a vault goes by: its display name when it has
// one, else the folder's own.
func resolveVaultName(settings VaultSettings, root string) string {
	if name := normalizeVaultDisplayName(settings.DisplayName); name != "" {
		return name
	}
	return filepath.Base(root)
}
