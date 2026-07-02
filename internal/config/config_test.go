package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAuthTokenFromFile(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	const want = "from-file-token-xxxxxx"
	if err := os.WriteFile(tokenFile, []byte("  "+want+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZENNOTES_AUTH_TOKEN", "")
	t.Setenv("ZENNOTES_AUTH_TOKEN_FILE", tokenFile)
	t.Setenv("ZENNOTES_CONFIG_PATH", filepath.Join(t.TempDir(), "missing.json"))

	cfg := Load()
	if cfg.AuthToken != want {
		t.Fatalf("AuthToken = %q, want %q (whitespace must be trimmed)", cfg.AuthToken, want)
	}
	if cfg.AuthTokenSource != AuthTokenSourceFile {
		t.Fatalf("AuthTokenSource = %q, want %q", cfg.AuthTokenSource, AuthTokenSourceFile)
	}
}

func TestEnvAuthTokenWinsOverFile(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("from-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZENNOTES_AUTH_TOKEN", "from-env")
	t.Setenv("ZENNOTES_AUTH_TOKEN_FILE", tokenFile)
	t.Setenv("ZENNOTES_CONFIG_PATH", filepath.Join(t.TempDir(), "missing.json"))

	cfg := Load()
	if cfg.AuthToken != "from-env" {
		t.Fatalf("AuthToken = %q, want from-env", cfg.AuthToken)
	}
	if cfg.AuthTokenSource != AuthTokenSourceEnv {
		t.Fatalf("AuthTokenSource = %q, want %q", cfg.AuthTokenSource, AuthTokenSourceEnv)
	}
}

// #304: a ZENNOTES_AUTH_TOKEN_FILE pointing at a missing/unreadable path must
// not set a token (and must not panic); the read error is logged so the failure
// is visible rather than surfacing as a misleading "missing token" error.
func TestAuthTokenFileMissingIsIgnoredNotFatal(t *testing.T) {
	t.Setenv("ZENNOTES_AUTH_TOKEN", "")
	t.Setenv("ZENNOTES_AUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "does-not-exist"))
	t.Setenv("ZENNOTES_CONFIG_PATH", filepath.Join(t.TempDir(), "missing.json"))

	cfg := Load()
	if cfg.AuthToken != "" {
		t.Fatalf("AuthToken = %q, want empty for an unreadable file", cfg.AuthToken)
	}
	if cfg.AuthTokenSource != AuthTokenSourceNone {
		t.Fatalf("AuthTokenSource = %q, want %q", cfg.AuthTokenSource, AuthTokenSourceNone)
	}
}

// An empty (or whitespace-only) token file loads no token rather than an empty one.
func TestAuthTokenFileEmptyLoadsNoToken(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZENNOTES_AUTH_TOKEN", "")
	t.Setenv("ZENNOTES_AUTH_TOKEN_FILE", tokenFile)
	t.Setenv("ZENNOTES_CONFIG_PATH", filepath.Join(t.TempDir(), "missing.json"))

	cfg := Load()
	if cfg.AuthToken != "" {
		t.Fatalf("AuthToken = %q, want empty for a whitespace-only file", cfg.AuthToken)
	}
	if cfg.AuthTokenSource != AuthTokenSourceNone {
		t.Fatalf("AuthTokenSource = %q, want %q", cfg.AuthTokenSource, AuthTokenSourceNone)
	}
}

func TestLoadDefaultLimitsAndModes(t *testing.T) {
	t.Setenv("ZENNOTES_AUTH_TOKEN", "")
	t.Setenv("ZENNOTES_CONFIG_PATH", filepath.Join(t.TempDir(), "missing.json"))
	cfg := Load()
	if cfg.MaxAssetBytes != defaultMaxAssetBytes {
		t.Errorf("MaxAssetBytes default = %d, want %d", cfg.MaxAssetBytes, defaultMaxAssetBytes)
	}
	if cfg.MaxNoteBytes != defaultMaxNoteBytes {
		t.Errorf("MaxNoteBytes default = %d, want %d", cfg.MaxNoteBytes, defaultMaxNoteBytes)
	}
	if cfg.VaultFileMode != defaultVaultFileMode {
		t.Errorf("VaultFileMode default = %v, want %v", cfg.VaultFileMode, defaultVaultFileMode)
	}
	if cfg.VaultDirMode != defaultVaultDirMode {
		t.Errorf("VaultDirMode default = %v, want %v", cfg.VaultDirMode, defaultVaultDirMode)
	}
}

func TestParseCIDRListEnv(t *testing.T) {
	t.Setenv("X", "127.0.0.1/32, 10.0.0.0/8 ,bad,192.168.1.5")
	got := parseCIDRListEnv("X")
	if len(got) != 3 {
		t.Fatalf("expected 3 valid entries, got %d: %+v", len(got), got)
	}
	// 192.168.1.5 (bare) should be expanded to /32.
	last := got[2]
	ones, bits := last.Mask.Size()
	if ones != 32 || bits != 32 {
		t.Fatalf("bare IP should be /32, got /%d (bits=%d)", ones, bits)
	}
	if !last.Contains(last.IP) {
		t.Fatalf("Net should contain its own IP")
	}
}

func TestNormalizeBasePath(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{"/", ""},
		{"//", ""},
		{"zennotes", "/zennotes"},
		{"/zennotes", "/zennotes"},
		{"/zennotes/", "/zennotes"},
		{"/zennotes//", "/zennotes"},
		{"/foo/bar", "/foo/bar"},
		{"/foo//bar/", "/foo/bar"},
		{" /apps/notes ", "/apps/notes"},
	}
	for _, c := range cases {
		if got := NormalizeBasePath(c.raw); got != c.want {
			t.Errorf("NormalizeBasePath(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestLoadBasePathFromEnv(t *testing.T) {
	t.Setenv("ZENNOTES_AUTH_TOKEN", "")
	t.Setenv("ZENNOTES_CONFIG_PATH", filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv("ZENNOTES_BASE_PATH", "/zennotes/")
	cfg := Load()
	if cfg.BasePath != "/zennotes" {
		t.Fatalf("BasePath = %q, want /zennotes (trailing slash trimmed)", cfg.BasePath)
	}
}

func TestParseFileModeEnv(t *testing.T) {
	cases := []struct {
		raw  string
		want fs.FileMode
		ok   bool
	}{
		{"", 0, false},
		{"600", 0o600, true},
		{"0600", 0o600, true},
		{"0o600", 0, false}, // not octal-prefix syntax
		{"755", 0o755, true},
		{"abc", 0, false},
	}
	for _, c := range cases {
		t.Setenv("X", c.raw)
		got, ok := parseFileModeEnv("X")
		if ok != c.ok || got != c.want {
			t.Errorf("parseFileModeEnv(%q) = (%v, %v), want (%v, %v)", c.raw, got, ok, c.want, c.ok)
		}
	}
}

// #sessions: ZENNOTES_PERSIST_SESSIONS is opt-in (off by default), and the
// sessions file sits next to the host config.
func TestPersistSessionsFlagAndPath(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "server.json")
	t.Setenv("ZENNOTES_AUTH_TOKEN", "x")
	t.Setenv("ZENNOTES_CONFIG_PATH", cfgPath)

	t.Setenv("ZENNOTES_PERSIST_SESSIONS", "")
	if Load().PersistSessions {
		t.Fatal("PersistSessions should default off")
	}
	t.Setenv("ZENNOTES_PERSIST_SESSIONS", "1")
	if !Load().PersistSessions {
		t.Fatal("ZENNOTES_PERSIST_SESSIONS=1 should enable it")
	}
	if got, want := SessionsPath(), filepath.Join(filepath.Dir(cfgPath), "sessions.json"); got != want {
		t.Fatalf("SessionsPath = %q, want %q", got, want)
	}
}
