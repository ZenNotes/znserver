package vault

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var ErrPathEscape = errors.New("path escapes vault root")

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

// ToPosix converts an OS-native path to forward-slash form.
func ToPosix(p string) string {
	return filepath.ToSlash(p)
}
