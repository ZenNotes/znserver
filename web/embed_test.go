//go:build embed_web

package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

func TestEmbeddedBrowserEntrypoint(t *testing.T) {
	bundle, err := Dist()
	if err != nil {
		t.Fatal(err)
	}
	index, err := fs.ReadFile(bundle, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	// Vite emits local module scripts and stylesheets in the entry document.
	// Check the actual distribution so an incomplete staging step fails CI.
	references := regexp.MustCompile(`(?:src|href)="((?:\./|/)?assets/[^"?#]+)(?:[?#][^"]*)?"`).FindAllSubmatch(index, -1)
	if len(references) == 0 {
		t.Fatal("index.html does not reference any bundled browser assets")
	}
	for _, reference := range references {
		path := strings.TrimPrefix(strings.TrimPrefix(string(reference[1]), "./"), "/")
		info, err := fs.Stat(bundle, path)
		if err != nil {
			t.Errorf("entrypoint asset %s: %v", path, err)
		} else if info.IsDir() || info.Size() == 0 {
			t.Errorf("entrypoint asset %s is not a nonempty file", path)
		}
	}
}
