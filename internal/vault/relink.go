package vault

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// rebaseMovedLink keeps a symlink pointing where it did after a rename from
// `from` to `to`. A link's relative text is read from the link's own folder,
// so one moved verbatim to another depth names a different file, or none:
// that is what mv does, and it is wrong for a note the user expects to keep
// reading the same file wherever it is filed. The text is re-based on the
// target it named from the old folder, which is also what makes a rename back
// (a rollback) land on the original text again. An absolute text needs
// nothing; links inside a moved folder move with their folder and are not
// followed. Mirrors rebaseMovedLink in apps/desktop/src/main/note-sidecars.ts
// (ZenNotes/zennotes), so a vault behaves the same wherever it is moved from.
func rebaseMovedLink(from, to string) error {
	info, err := os.Lstat(to)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	text, err := os.Readlink(to)
	if err != nil {
		return err
	}
	if filepath.IsAbs(text) {
		return nil
	}
	fromDir, toDir := filepath.Dir(from), filepath.Dir(to)
	if fromDir == toDir {
		return nil
	}
	next, err := filepath.Rel(toDir, filepath.Join(fromDir, text))
	if err != nil {
		return err
	}
	if next == text {
		return nil
	}
	// A new link beside it, renamed over: no moment without a link at `to`.
	temporary := fmt.Sprintf("%s_relink_tmp_%d_%d", to, os.Getpid(), time.Now().UnixNano())
	if err := os.Symlink(next, temporary); err != nil {
		return err
	}
	if err := os.Rename(temporary, to); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}
