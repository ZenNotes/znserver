package vault

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSafeJoinLexical(t *testing.T) {
	root := t.TempDir()

	cases := []struct {
		name    string
		rel     string
		wantErr error
	}{
		{"plain file", "note.md", nil},
		{"nested", "a/b/c.md", nil},
		{"leading slash", "/note.md", nil},
		{"dot only", ".", nil},
		// Leading ".." is neutralised by the leading "/" anchor before
		// Clean(), so these resolve safely inside root rather than
		// escaping. The escape path is exercised via symlinks in the
		// dedicated tests below.
		{"parent neutralised", "../escape.md", nil},
		{"deep parent neutralised", "a/../../escape.md", nil},
		{"absolute neutralised", "/../../etc/passwd", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			abs, err := SafeJoin(root, tc.rel)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected %v, got err=%v abs=%q", tc.wantErr, err, abs)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			rootAbs, _ := filepath.Abs(root)
			if !strings.HasPrefix(abs, rootAbs) {
				t.Fatalf("result %q is not under root %q", abs, rootAbs)
			}
		})
	}
}

func TestSafeJoinSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(target, []byte("hush"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "evil.md")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if _, err := SafeJoin(root, "evil.md"); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("expected ErrPathEscape, got %v", err)
	}
}

func TestSafeJoinSymlinkInsideVault(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	root := t.TempDir()
	target := filepath.Join(root, "real.md")
	if err := os.WriteFile(target, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "alias.md")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	abs, err := SafeJoin(root, "alias.md")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if abs == "" {
		t.Fatal("empty result")
	}
}

func TestSafeJoinSymlinkInPathSegment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	root := t.TempDir()
	outside := t.TempDir()

	// /root/sneaky -> /outside  (symlinked directory)
	if err := os.Symlink(outside, filepath.Join(root, "sneaky")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "leak.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := SafeJoin(root, "sneaky/leak.md"); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("expected ErrPathEscape on symlinked subdir, got %v", err)
	}
}

func TestSafeJoinNonexistentTail(t *testing.T) {
	root := t.TempDir()
	abs, err := SafeJoin(root, "newfolder/new.md")
	if err != nil {
		t.Fatalf("expected success for non-existent path under root, got %v", err)
	}
	rootAbs, _ := filepath.Abs(root)
	if !strings.HasPrefix(abs, rootAbs) {
		t.Fatalf("result %q is not under root %q", abs, rootAbs)
	}
}

func TestSafeJoinRootMissing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "does-not-exist-yet")
	abs, err := SafeJoin(root, "note.md")
	if err != nil {
		t.Fatalf("expected lexical fallback when root absent, got %v", err)
	}
	if !strings.HasSuffix(abs, "note.md") {
		t.Fatalf("unexpected path %q", abs)
	}
}

func TestSafeDestinationRefusesEveryLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows")
	}
	base := t.TempDir()
	root := filepath.Join(base, "vault")
	for _, dir := range []string{"Projects/Real", "Journal"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "Projects", "Plan.md"), []byte("# plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"inner":    filepath.Join("..", "Journal"),
		"out":      base,
		"dangling": filepath.Join("..", "Gone"),
		"file.md":  "Plan.md",
	} {
		if err := os.Symlink(target, filepath.Join(root, "Projects", name)); err != nil {
			t.Fatal(err)
		}
	}

	for rel, want := range map[string]error{
		"Projects/inner":         ErrThroughSymlink,
		"Projects/inner/New":     ErrThroughSymlink,
		"Projects/inner/New/Sub": ErrThroughSymlink,
		"Projects/dangling":      os.ErrNotExist, // SafeJoin cannot resolve it
		"Projects/dangling/New":  os.ErrNotExist,
		"Projects/file.md":       ErrThroughSymlink,
		"Projects/out":           ErrPathEscape,
		"Projects/out/New":       ErrPathEscape,
		"../Journal":             nil, // cleaned to inside the root, as in SafeJoin
		"":                       nil,
		"Projects":               nil,
		"Projects/Real":          nil,
		"Projects/Real/New/Sub":  nil,
		"Projects/New":           nil,
		"Projects/Plan.md":       nil, // the file's own failure comes from the write
	} {
		if _, err := SafeDestination(root, rel); !errors.Is(err, want) {
			t.Errorf("SafeDestination(%q) = %v, want %v", rel, err, want)
		}
	}
}

// The root is not examined: a vault that is itself a link keeps working.
func TestSafeDestinationAcceptsALinkedRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows")
	}
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(filepath.Join(real, "Projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := SafeDestination(link, "Projects/New"); err != nil {
		t.Fatalf("a linked root was refused: %v", err)
	}
}
