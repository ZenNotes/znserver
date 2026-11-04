package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// minMCPTokenLength rules out a token someone could guess, and the output of
// `openssl rand -hex 32`, which the docs suggest, is twice it.
const minMCPTokenLength = 32

// MCPCredential is one MCP token, held only as its hash, and the scope it grants.
type MCPCredential struct {
	Name        string
	TokenSHA256 [sha256.Size]byte
	Access      string
	Allow, Deny []string
}

// MCPConfig is the built-in MCP endpoint. It is off unless ZENNOTES_MCP_ENABLED
// says otherwise, and with it off nothing below is read.
type MCPConfig struct {
	Enabled        bool
	Credentials    []MCPCredential
	AllowedOrigins []string
}

// mcpCredentialsFile is ZENNOTES_MCP_CREDENTIALS_FILE: several agents, each with
// its own token and scope.
type mcpCredentialsFile struct {
	Version     int `json:"version"`
	Credentials []struct {
		Name        string   `json:"name"`
		TokenSHA256 string   `json:"tokenSha256"`
		Access      string   `json:"access"`
		Allow       []string `json:"allow"`
		Deny        []string `json:"deny"`
	} `json:"credentials"`
}

const mcpCredentialsFileVersion = 1

// A name goes into the log, so it stays on one line and is not mistaken for
// anything else there.
var mcpCredentialName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// LoadMCP reads the MCP settings. An error is a setting someone meant to work and
// that cannot, and the server refuses to start: a mistyped scope must never become
// a wider one. Errors name variables and files, never a token.
func LoadMCP() (MCPConfig, error) {
	if !envEnabled("ZENNOTES_MCP_ENABLED") {
		return MCPConfig{}, nil
	}
	origins := parseListEnv("ZENNOTES_MCP_ALLOWED_ORIGINS")
	for _, origin := range origins {
		if origin == "*" {
			return MCPConfig{}, errors.New("ZENNOTES_MCP_ALLOWED_ORIGINS cannot be *: list the origins, or leave it unset, as nearly every MCP client sends no Origin")
		}
	}
	var (
		credentials []MCPCredential
		err         error
	)
	if path := os.Getenv("ZENNOTES_MCP_CREDENTIALS_FILE"); path != "" {
		for _, name := range []string{"ZENNOTES_MCP_TOKEN", "ZENNOTES_MCP_TOKEN_FILE", "ZENNOTES_MCP_ACCESS", "ZENNOTES_MCP_ALLOW_PATHS", "ZENNOTES_MCP_DENY_PATHS"} {
			if os.Getenv(name) != "" {
				return MCPConfig{}, fmt.Errorf("ZENNOTES_MCP_CREDENTIALS_FILE and %s cannot be used together: a credentials file holds every token and scope", name)
			}
		}
		credentials, err = readMCPCredentialsFile(path)
	} else {
		credentials, err = mcpCredentialFromEnv()
	}
	if err != nil {
		return MCPConfig{}, err
	}
	return MCPConfig{Enabled: true, Credentials: credentials, AllowedOrigins: origins}, nil
}

func mcpCredentialFromEnv() ([]MCPCredential, error) {
	token := strings.TrimSpace(os.Getenv("ZENNOTES_MCP_TOKEN"))
	if path := os.Getenv("ZENNOTES_MCP_TOKEN_FILE"); path != "" {
		if token != "" {
			return nil, errors.New("set ZENNOTES_MCP_TOKEN or ZENNOTES_MCP_TOKEN_FILE, not both")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("ZENNOTES_MCP_TOKEN_FILE could not be read: %w", err)
		}
		token = strings.TrimSpace(string(raw))
	}
	if token == "" {
		return nil, errors.New("ZENNOTES_MCP_ENABLED is on with no token: set ZENNOTES_MCP_TOKEN, ZENNOTES_MCP_TOKEN_FILE or ZENNOTES_MCP_CREDENTIALS_FILE")
	}
	if len(token) < minMCPTokenLength {
		return nil, fmt.Errorf("the MCP token has fewer than %d characters; make one with: openssl rand -hex 32", minMCPTokenLength)
	}
	allow, err := parseMCPPathList("ZENNOTES_MCP_ALLOW_PATHS")
	if err != nil {
		return nil, err
	}
	deny, err := parseMCPPathList("ZENNOTES_MCP_DENY_PATHS")
	if err != nil {
		return nil, err
	}
	return []MCPCredential{{
		Name:        "default",
		TokenSHA256: sha256.Sum256([]byte(token)),
		Access:      os.Getenv("ZENNOTES_MCP_ACCESS"),
		Allow:       allow,
		Deny:        deny,
	}}, nil
}

// parseMCPPathList reads a JSON array. The server's usual list format splits on
// commas, which would turn a folder called "My, Folder" into an allow entry for
// "My" and could share the wrong folder.
func parseMCPPathList(name string) ([]string, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil, nil
	}
	var paths []string
	if err := json.Unmarshal([]byte(raw), &paths); err != nil {
		return nil, fmt.Errorf(`%s must be a JSON array of paths, such as ["Projects","Areas/Health"]: %w`, name, err)
	}
	return paths, nil
}

func readMCPCredentialsFile(path string) ([]MCPCredential, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("ZENNOTES_MCP_CREDENTIALS_FILE could not be read: %w", err)
	}
	// An unknown field is refused: a misspelled "allow" would otherwise leave a
	// credential seeing the whole vault.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var file mcpCredentialsFile
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("ZENNOTES_MCP_CREDENTIALS_FILE: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("ZENNOTES_MCP_CREDENTIALS_FILE: unexpected data after the JSON object")
	}
	if file.Version != mcpCredentialsFileVersion {
		return nil, fmt.Errorf("ZENNOTES_MCP_CREDENTIALS_FILE: version %d is not supported; this server reads version %d", file.Version, mcpCredentialsFileVersion)
	}
	if len(file.Credentials) == 0 {
		return nil, errors.New("ZENNOTES_MCP_CREDENTIALS_FILE lists no credentials")
	}
	credentials := make([]MCPCredential, 0, len(file.Credentials))
	names := map[string]bool{}
	for index, entry := range file.Credentials {
		if !mcpCredentialName.MatchString(entry.Name) {
			return nil, fmt.Errorf("ZENNOTES_MCP_CREDENTIALS_FILE: credential %d needs a name of letters, digits, dot, dash or underscore", index+1)
		}
		if names[entry.Name] {
			return nil, fmt.Errorf("ZENNOTES_MCP_CREDENTIALS_FILE: the name %q appears twice", entry.Name)
		}
		names[entry.Name] = true
		digest, err := hex.DecodeString(entry.TokenSHA256)
		if err != nil || len(digest) != sha256.Size {
			return nil, fmt.Errorf("ZENNOTES_MCP_CREDENTIALS_FILE: credential %q needs tokenSha256, the 64 hex digits of the token's SHA-256", entry.Name)
		}
		credential := MCPCredential{Name: entry.Name, Access: entry.Access, Allow: entry.Allow, Deny: entry.Deny}
		copy(credential.TokenSHA256[:], digest)
		credentials = append(credentials, credential)
	}
	return credentials, nil
}
