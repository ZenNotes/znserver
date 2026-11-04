package config

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testMCPToken = "mcp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func clearMCPEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"ZENNOTES_MCP_ENABLED", "ZENNOTES_MCP_TOKEN", "ZENNOTES_MCP_TOKEN_FILE", "ZENNOTES_MCP_ACCESS",
		"ZENNOTES_MCP_ALLOW_PATHS", "ZENNOTES_MCP_DENY_PATHS", "ZENNOTES_MCP_CREDENTIALS_FILE", "ZENNOTES_MCP_ALLOWED_ORIGINS",
	} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
	t.Setenv("ZENNOTES_VAULT_PATH", "/vault")
}

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMCPIsOffUnlessEnabled(t *testing.T) {
	clearMCPEnv(t)
	for _, value := range []string{"", "0", "false", "off", "maybe"} {
		t.Setenv("ZENNOTES_MCP_ENABLED", value)
		// Settings that would be refused if it were on do not matter while it is off.
		t.Setenv("ZENNOTES_MCP_TOKEN", "short")
		m, err := LoadMCP()
		if err != nil || m.Enabled {
			t.Errorf("ZENNOTES_MCP_ENABLED=%q: %+v, %v", value, m, err)
		}
	}
}

func TestMCPCredentialFromEnv(t *testing.T) {
	clearMCPEnv(t)
	t.Setenv("ZENNOTES_MCP_ENABLED", "1")
	t.Setenv("ZENNOTES_MCP_TOKEN", "  "+testMCPToken+"\n")
	t.Setenv("ZENNOTES_MCP_ACCESS", "read")
	t.Setenv("ZENNOTES_MCP_ALLOW_PATHS", `["Projects","My, Folder"]`)
	t.Setenv("ZENNOTES_MCP_DENY_PATHS", `["Projects/Clients"]`)
	t.Setenv("ZENNOTES_MCP_ALLOWED_ORIGINS", "https://a.example, https://b.example")
	m, err := LoadMCP()
	if err != nil {
		t.Fatal(err)
	}
	if !m.Enabled || len(m.Credentials) != 1 || len(m.AllowedOrigins) != 2 {
		t.Fatalf("%+v", m)
	}
	c := m.Credentials[0]
	if c.TokenSHA256 != sha256.Sum256([]byte(testMCPToken)) {
		t.Error("the token was not trimmed before hashing")
	}
	// A comma in a folder name is one folder.
	if c.Access != "read" || len(c.Allow) != 2 || c.Allow[1] != "My, Folder" || len(c.Deny) != 1 {
		t.Errorf("%+v", c)
	}
}

func TestMCPTokenFile(t *testing.T) {
	clearMCPEnv(t)
	t.Setenv("ZENNOTES_MCP_ENABLED", "1")
	t.Setenv("ZENNOTES_MCP_ACCESS", "read")
	t.Setenv("ZENNOTES_MCP_TOKEN_FILE", writeTemp(t, testMCPToken+"\n"))
	m, err := LoadMCP()
	if err != nil || m.Credentials[0].TokenSHA256 != sha256.Sum256([]byte(testMCPToken)) {
		t.Fatalf("%+v %v", m, err)
	}
	t.Setenv("ZENNOTES_MCP_TOKEN", testMCPToken)
	if _, err := LoadMCP(); err == nil {
		t.Error("a token and a token file together were accepted")
	}
}

func TestMCPCredentialsFile(t *testing.T) {
	clearMCPEnv(t)
	t.Setenv("ZENNOTES_MCP_ENABLED", "1")
	digest := sha256.Sum256([]byte(testMCPToken))
	sum := hex.EncodeToString(digest[:])
	t.Setenv("ZENNOTES_MCP_CREDENTIALS_FILE", writeTemp(t, `{"version":1,"credentials":[{"name":"chat-gpt.1","tokenSha256":"`+sum+`","access":"comment","allow":["Areas"],"deny":["Areas/Health"]}]}`))
	m, err := LoadMCP()
	if err != nil {
		t.Fatal(err)
	}
	c := m.Credentials[0]
	if c.Name != "chat-gpt.1" || c.TokenSHA256 != digest || c.Access != "comment" || c.Allow[0] != "Areas" || c.Deny[0] != "Areas/Health" {
		t.Errorf("%+v", c)
	}
}

func TestMCPRefusals(t *testing.T) {
	digest := sha256.Sum256([]byte(testMCPToken))
	sum := hex.EncodeToString(digest[:])
	entry := func(extra string) string {
		return `{"name":"a","tokenSha256":"` + sum + `","access":"read"` + extra + `}`
	}
	cases := []struct {
		name string
		env  map[string]string
		file string
		want string
	}{
		{name: "nothing", want: "no token"},
		{name: "no vault path", env: map[string]string{"ZENNOTES_MCP_TOKEN": testMCPToken, "ZENNOTES_VAULT_PATH": ""}, want: "ZENNOTES_VAULT_PATH"},
		{name: "a blank vault path", env: map[string]string{"ZENNOTES_MCP_TOKEN": testMCPToken, "ZENNOTES_VAULT_PATH": "  "}, want: "ZENNOTES_VAULT_PATH"},
		{name: "allow blank", env: map[string]string{"ZENNOTES_MCP_TOKEN": testMCPToken, "ZENNOTES_MCP_ALLOW_PATHS": ""}, want: "JSON array"},
		{name: "allow empty", env: map[string]string{"ZENNOTES_MCP_TOKEN": testMCPToken, "ZENNOTES_MCP_ALLOW_PATHS": "[]"}, want: "is empty"},
		{name: "allow null", env: map[string]string{"ZENNOTES_MCP_TOKEN": testMCPToken, "ZENNOTES_MCP_ALLOW_PATHS": "null"}, want: "is empty"},
		{name: "deny blank", env: map[string]string{"ZENNOTES_MCP_TOKEN": testMCPToken, "ZENNOTES_MCP_DENY_PATHS": ""}, want: "JSON array"},
		{name: "deny empty", env: map[string]string{"ZENNOTES_MCP_TOKEN": testMCPToken, "ZENNOTES_MCP_DENY_PATHS": "[]"}, want: "is empty"},
		{name: "deny null", env: map[string]string{"ZENNOTES_MCP_TOKEN": testMCPToken, "ZENNOTES_MCP_DENY_PATHS": "null"}, want: "is empty"},
		{name: "file deny empty", file: `{"version":1,"credentials":[` + entry(`,"deny":[]`) + `]}`, want: "is empty"},
		{name: "file deny null", file: `{"version":1,"credentials":[` + entry(`,"deny":null`) + `]}`, want: "is empty"},
		{name: "file allow empty", file: `{"version":1,"credentials":[` + entry(`,"allow":[]`) + `]}`, want: "is empty"},
		{name: "file allow null", file: `{"version":1,"credentials":[` + entry(`,"allow":null`) + `]}`, want: "is empty"},
		{name: "short", env: map[string]string{"ZENNOTES_MCP_TOKEN": "short"}, want: "fewer than 32"},
		{name: "missing token file", env: map[string]string{"ZENNOTES_MCP_TOKEN_FILE": "/does/not/exist"}, want: "could not be read"},
		{name: "empty token file", env: map[string]string{"ZENNOTES_MCP_TOKEN_FILE": writeTemp(t, " \n")}, want: "no token"},
		{name: "allow not JSON", env: map[string]string{"ZENNOTES_MCP_TOKEN": testMCPToken, "ZENNOTES_MCP_ALLOW_PATHS": "Projects,Areas"}, want: "JSON array"},
		{name: "deny a string", env: map[string]string{"ZENNOTES_MCP_TOKEN": testMCPToken, "ZENNOTES_MCP_DENY_PATHS": `"Projects"`}, want: "JSON array"},
		{name: "any origin", env: map[string]string{"ZENNOTES_MCP_TOKEN": testMCPToken, "ZENNOTES_MCP_ALLOWED_ORIGINS": "*"}, want: "cannot be *"},
		{name: "file with token", env: map[string]string{"ZENNOTES_MCP_TOKEN": testMCPToken}, file: `{"version":1,"credentials":[` + entry("") + `]}`, want: "cannot be used together"},
		{name: "file with access", env: map[string]string{"ZENNOTES_MCP_ACCESS": "read"}, file: `{"version":1,"credentials":[` + entry("") + `]}`, want: "cannot be used together"},
		{name: "invalid", file: `{`, want: "CREDENTIALS_FILE"},
		{name: "trailing data", file: `{"version":1,"credentials":[` + entry("") + `]} {}`, want: "after the JSON"},
		{name: "no version", file: `{"credentials":[` + entry("") + `]}`, want: "version 0"},
		{name: "future version", file: `{"version":2,"credentials":[` + entry("") + `]}`, want: "version 2"},
		{name: "misspelled field", file: `{"version":1,"credentials":[` + entry(`,"alow":["Projects"]`) + `]}`, want: "unknown field"},
		{name: "plain token", file: `{"version":1,"credentials":[` + entry(`,"token":"x"`) + `]}`, want: "unknown field"},
		{name: "empty", file: `{"version":1,"credentials":[]}`, want: "no credentials"},
		{name: "bad name", file: `{"version":1,"credentials":[{"name":"a b","tokenSha256":"` + sum + `","access":"read"}]}`, want: "needs a name"},
		{name: "log injection", file: `{"version":1,"credentials":[{"name":"a\nb","tokenSha256":"` + sum + `","access":"read"}]}`, want: "needs a name"},
		{name: "short hash", file: `{"version":1,"credentials":[{"name":"a","tokenSha256":"abcd","access":"read"}]}`, want: "tokenSha256"},
		{name: "not hex", file: `{"version":1,"credentials":[{"name":"a","tokenSha256":"` + strings.Repeat("z", 64) + `","access":"read"}]}`, want: "tokenSha256"},
		{name: "same name", file: `{"version":1,"credentials":[` + entry("") + `,` + entry("") + `]}`, want: "appears twice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearMCPEnv(t)
			t.Setenv("ZENNOTES_MCP_ENABLED", "1")
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			if tc.file != "" {
				t.Setenv("ZENNOTES_MCP_CREDENTIALS_FILE", writeTemp(t, tc.file))
			}
			_, err := LoadMCP()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), testMCPToken) {
				t.Errorf("the error holds the token: %v", err)
			}
		})
	}
}
