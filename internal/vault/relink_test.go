package vault

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A note that is a relative link, moved into a subfolder: moved verbatim its
// text would name nothing from there.
func TestMoveNoteKeepsARelativeLinkPointingAtItsFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows")
	}
	v, root := workflowTestVault(t)
	if err := os.MkdirAll(filepath.Join(root, "inbox", "sources"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "inbox", "sources", "Real.md"), []byte("# Real\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("sources", "Real.md"), filepath.Join(root, "inbox", "Rel.md")); err != nil {
		t.Fatal(err)
	}

	moved, err := v.MoveNote("inbox/Rel.md", "inbox", "Topics")
	if err != nil {
		t.Fatal(err)
	}
	if moved.Path != "inbox/Topics/Rel.md" {
		t.Fatalf("moved to %s", moved.Path)
	}
	text, err := os.Readlink(filepath.Join(root, "inbox", "Topics", "Rel.md"))
	if err != nil {
		t.Fatal(err)
	}
	if text != filepath.Join("..", "sources", "Real.md") {
		t.Fatalf("link text = %q", text)
	}
	body, err := os.ReadFile(filepath.Join(root, "inbox", "Topics", "Rel.md"))
	if err != nil || string(body) != "# Real\n" {
		t.Fatalf("the moved link does not read its file: %v %q", err, body)
	}
}

// Renamed back the same way, the text comes back as it was, which is what a
// rollback relies on.
func TestRebaseMovedLinkRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows")
	}
	root := t.TempDir()
	for _, dir := range []string{"sources", "inbox/Topics"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	from := filepath.Join(root, "inbox", "Rel.md")
	to := filepath.Join(root, "inbox", "Topics", "Rel.md")
	if err := os.Symlink(filepath.Join("..", "sources", "Real.md"), from); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
	if err := rebaseMovedLink(from, to); err != nil {
		t.Fatal(err)
	}
	if text, _ := os.Readlink(to); text != filepath.Join("..", "..", "sources", "Real.md") {
		t.Fatalf("after the move, text = %q", text)
	}
	if err := os.Rename(to, from); err != nil {
		t.Fatal(err)
	}
	if err := rebaseMovedLink(to, from); err != nil {
		t.Fatal(err)
	}
	if text, _ := os.Readlink(from); text != filepath.Join("..", "sources", "Real.md") {
		t.Fatalf("after the move back, text = %q", text)
	}
	// An absolute text is left alone.
	abs := filepath.Join(root, "sources", "Real.md")
	if err := os.Symlink(abs, from+".abs"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(from+".abs", to+".abs"); err != nil {
		t.Fatal(err)
	}
	if err := rebaseMovedLink(from+".abs", to+".abs"); err != nil {
		t.Fatal(err)
	}
	if text, _ := os.Readlink(to + ".abs"); text != abs {
		t.Fatalf("absolute text changed to %q", text)
	}
}
