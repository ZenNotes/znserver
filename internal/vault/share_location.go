package vault

import (
	"encoding/json"
	"strings"
)

// Where a share from the macOS Share menu is filed, per vault (2.79):
// `sharesLocation` in vault.json. Byte-for-byte mirror of normalizeShareLocation
// in packages/shared-domain/src/share-location.ts: change both together. The
// server never files a share itself; the desktop app reads the setting through
// GET /vault/settings and creates the note with the (folder, subpath) pair
// resolveShareLocation gives it. So this side only has to keep the value
// exactly as the desktop would, or a server vault's choice is lost on the next
// settings save and every share lands in Quick Notes.
//
// The normalizer does not check the folder for traversal, absolute paths or
// reserved names, and neither does the TypeScript one: CreateNote's SafeJoin
// does, on the subpath the desktop sends, as it does for every other location
// setting.

// normalizeShareLocation is the setting as it is stored and served: an unknown
// mode is Quick Notes; primary carries no folder; folder mode keeps an empty
// folder, so Settings can show the folder input while it is being typed (until
// a folder is named it files like primary), and loses the whitespace JavaScript's
// trim() removes and then any leading and trailing slashes, in that order.
func normalizeShareLocation(value ShareLocationSetting) ShareLocationSetting {
	switch value.Mode {
	case ShareLocationPrimary:
		return ShareLocationSetting{Mode: ShareLocationPrimary}
	case ShareLocationFolder:
		folder := strings.Trim(strings.TrimFunc(value.Folder, isJSWhitespace), "/")
		return ShareLocationSetting{Mode: ShareLocationFolder, Folder: folder}
	default:
		return ShareLocationSetting{Mode: ShareLocationQuick}
	}
}

// UnmarshalJSON reads the setting the way the TypeScript normalizer reads an
// `unknown`: a value that is not an object, a mode that is not a string and a
// folder that is not a string each count as absent. A plain struct decode
// would fail the whole vault.json on any of them, and every request that
// reads settings with it. Keys match exactly, as a JavaScript property read
// does, not case-insensitively as Go's struct decoding does.
func (s *ShareLocationSetting) UnmarshalJSON(data []byte) error {
	*s = ShareLocationSetting{}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return nil
	}
	var mode, folder string
	if json.Unmarshal(fields["mode"], &mode) == nil {
		s.Mode = ShareLocationMode(mode)
	}
	if json.Unmarshal(fields["folder"], &folder) == nil {
		s.Folder = folder
	}
	return nil
}

// MarshalJSON writes what the TypeScript normalizer returns: `folder` in
// folder mode only, and there always, an empty one included, so the server
// stores and serves the same bytes for a folder still being typed.
func (s ShareLocationSetting) MarshalJSON() ([]byte, error) {
	if s.Mode == ShareLocationFolder {
		return json.Marshal(struct {
			Mode   ShareLocationMode `json:"mode"`
			Folder string            `json:"folder"`
		}{s.Mode, s.Folder})
	}
	return json.Marshal(struct {
		Mode ShareLocationMode `json:"mode"`
	}{s.Mode})
}
