package vault

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// finishes fails the test when a call does not return. These calls hold the vault's
// lock, so a hang here stopped every note request on the server.
func finishes(t *testing.T, name string, call func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		call()
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s never returned", name)
	}
}

func newUniqueVault(t *testing.T) (*Vault, string) {
	t.Helper()
	root := t.TempDir()
	v, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return v, root
}

func TestNoteTitlesThatNoFileCanBeNamed(t *testing.T) {
	cases := map[string]string{
		"300 characters":  strings.Repeat("x", 300),
		"85 CJK":          strings.Repeat("漢", 90),
		"a NUL":           "a\x00b",
		"only a NUL":      "\x00",
		"newlines":        "a\nb\r\nc",
		"spaces at a cut": strings.Repeat("x", 199) + "   y",
	}
	for name, title := range cases {
		t.Run(name, func(t *testing.T) {
			v, _ := newUniqueVault(t)
			var meta NoteMeta
			var err error
			finishes(t, "CreateNote", func() { meta, err = v.CreateNote(FolderInbox, title, "") })
			if err != nil {
				t.Fatal(err)
			}
			base := filepath.Base(meta.Path)
			if len(base) > 255 || !utf8.ValidString(base) || strings.ContainsAny(base, "\x00\r\n") {
				t.Errorf("name %q", base)
			}
			// The same title again gets a number, still within the limit.
			var second NoteMeta
			finishes(t, "CreateNote", func() { second, err = v.CreateNote(FolderInbox, title, "") })
			if err != nil || second.Path == meta.Path {
				t.Errorf("second: %q, %v", second.Path, err)
			}
		})
	}
}

func TestUploadWithALongFileName(t *testing.T) {
	v, _ := newUniqueVault(t)
	for _, name := range []string{strings.Repeat("x", 300) + ".png", "x." + strings.Repeat("y", 300), strings.Repeat("漢", 120) + ".pdf"} {
		var asset ImportedAsset
		var err error
		finishes(t, "ImportAsset", func() { asset, err = v.ImportAsset("", name, strings.NewReader("data")) })
		if err != nil {
			t.Errorf("%.20s: %v", name, err)
			continue
		}
		if base := filepath.Base(asset.Path); len(base) > 255 || !utf8.ValidString(base) {
			t.Errorf("name %q", base)
		}
	}
	asset, err := v.ImportAsset("", strings.Repeat("x", 300)+".png", strings.NewReader("data"))
	if err != nil || !strings.HasSuffix(asset.Path, ".png") {
		t.Errorf("the extension was lost: %q, %v", asset.Path, err)
	}
}

func TestUniquePathStopsOnAnError(t *testing.T) {
	dir := t.TempDir()
	var err error
	// The filesystem's answer, on the first try, and not giving up after many.
	stopped := func(what string) {
		t.Helper()
		if err == nil || strings.Contains(err.Error(), "no free name") {
			t.Errorf("%s: %v", what, err)
		}
	}
	finishes(t, "uniquePath", func() { _, err = uniquePath(dir, strings.Repeat("x", 300), ".md") })
	stopped("a name too long")
	finishes(t, "uniquePath", func() { _, err = uniquePath(dir, "a\x00b", ".md") })
	stopped("a NUL")
	// A file where a folder should be.
	file := filepath.Join(dir, "file")
	if werr := os.WriteFile(file, nil, 0o600); werr != nil {
		t.Fatal(werr)
	}
	finishes(t, "uniquePath", func() { _, err = uniquePath(file, "a", ".md") })
	stopped("a path under a file")
	finishes(t, "uniqueDir", func() { _, err = uniqueDir(dir, strings.Repeat("x", 300)) })
	stopped("a folder name too long")
	finishes(t, "uniqueTemplateSlug", func() { _, err = uniqueTemplateSlug(dir, strings.Repeat("x", 300), "") })
	stopped("a template name too long")
}

func TestUniquePathInAnUnreadableFolder(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("permissions do not stop this user")
	}
	v, root := newUniqueVault(t)
	locked := filepath.Join(root, "Locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	var err error
	finishes(t, "CreateNote", func() { _, err = v.CreateNote(FolderInbox, "Note", "Locked") })
	if err == nil {
		t.Error("created a note in a folder nothing can read")
	}
}

func TestUniquePathGivesUp(t *testing.T) {
	dir := t.TempDir()
	stem := "n"
	for i := 2; i <= maxUniqueAttempts; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s %d.md", stem, i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, stem+".md"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	finishes(t, "uniquePath", func() { _, err = uniquePath(dir, stem, ".md") })
	if err == nil || errors.Is(err, os.ErrNotExist) {
		t.Errorf("every name taken: %v", err)
	}
}
