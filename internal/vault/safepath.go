package vault

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var ErrPathEscape = errors.New("path escapes vault root")

// ErrThroughSymlink is returned for a destination that goes through a symbolic
// link. See SafeDestination.
var ErrThroughSymlink = errors.New("path goes through a symbolic link")

// ErrIsDirectory is returned when a caller asks to read a directory as a file,
// which a client does by accident whenever it treats a `.base` database folder
// as a note. It is classified here, from a stat the read already performs,
// rather than from the errno the read returns: Unix answers EISDIR but Windows
// answers ERROR_INVALID_FUNCTION ("Incorrect function"), so an errno test makes
// the same request a 400 on one platform and a 500 on another.
var ErrIsDirectory = errors.New("path is a directory, not a file")

// SafeJoin cleans a user-supplied relative POSIX path and joins it onto
// `root`, refusing anything that resolves outside `root`. Any existing
// component of the joined path that is a symbolic link is resolved and
// must still resolve to a location inside `root`; otherwise ErrPathEscape
// is returned. Components that do not yet exist (write-create case) are
// left alone — they cannot be symlinks until they're created.
//
// The returned path is in the same namespace as `root` (i.e. rooted at
// the abs form of the caller's root, not the canonical form), so callers
// can still strip the root prefix to get a stable relative path.
func SafeJoin(root, rel string) (string, error) {
	if root == "" {
		return "", errors.New("root is empty")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	cleaned := filepath.Clean("/" + strings.TrimPrefix(rel, "/"))
	joined := filepath.Join(rootAbs, filepath.FromSlash(cleaned))

	relBack, err := filepath.Rel(rootAbs, joined)
	if err != nil {
		return "", err
	}
	if relBack == ".." || strings.HasPrefix(relBack, ".."+string(filepath.Separator)) {
		return "", ErrPathEscape
	}

	rootCanonical, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		// Root not on disk yet (e.g. caller is about to MkdirAll). Without
		// a canonical root we can't meaningfully evaluate symlink targets,
		// so fall back to the lexical-only result.
		return joined, nil
	}
	if relBack == "." {
		return joined, nil
	}

	parts := strings.Split(relBack, string(filepath.Separator))
	walk := rootAbs
	for _, part := range parts {
		walk = filepath.Join(walk, part)
		info, err := os.Lstat(walk)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return joined, nil
			}
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(walk)
			if err != nil {
				return "", err
			}
			relTarget, err := filepath.Rel(rootCanonical, target)
			if err != nil {
				return "", err
			}
			if relTarget == ".." || strings.HasPrefix(relTarget, ".."+string(filepath.Separator)) {
				return "", ErrPathEscape
			}
			walk = target
		}
	}
	return joined, nil
}

// SafeDestination is SafeJoin for a place a client names to create or move
// something into. On top of what SafeJoin refuses, every part of `rel` that
// already exists must be a real directory, so a link never takes a new file
// or folder somewhere other than the path it was asked for, even a place
// inside the vault.
//
// The listings skip links to folders, so a client sees a linked folder as a
// path with nothing there. `zn mcp --allow-path Projects` checks a destination
// against those listings, and could be walked into a hidden folder by a link
// at Projects/inner if the write followed it.
//
// Routes that take a note's own path keep using SafeJoin: read, write, rename,
// duplicate and delete. A link to a note file is a supported way to file a note
// in two places, and a client limiting itself to part of a vault only sends
// paths it found in the listings. `root` itself is not examined, so a vault or a
// system folder that is a link keeps working. The assets folder is the exception
// (ImportAsset), since its name is not one a client chooses.
//
// A regular file on the way is left to the operation that follows, which
// fails with ENOTDIR. Anything else is refused, which covers Windows
// junctions, which Lstat reports as neither a link nor a directory.
func SafeDestination(root, rel string) (string, error) {
	joined, err := SafeJoin(root, rel)
	if err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	relBack, err := filepath.Rel(rootAbs, joined)
	if err != nil {
		return "", err
	}
	if relBack == "." {
		return joined, nil
	}
	walk := rootAbs
	for _, part := range strings.Split(relBack, string(filepath.Separator)) {
		walk = filepath.Join(walk, part)
		info, err := os.Lstat(walk)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return joined, nil
			}
			return "", err
		}
		if mode := info.Mode(); !mode.IsDir() && !mode.IsRegular() {
			return "", ErrThroughSymlink
		}
	}
	return joined, nil
}

// ToPosix converts an OS-native path to forward-slash form.
func ToPosix(p string) string {
	return filepath.ToSlash(p)
}
