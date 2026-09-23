package znserver

import (
	"encoding/json"
	"os"
	"testing"
)

func TestVersionIsTheReleaseJSONVersion(t *testing.T) {
	raw, err := os.ReadFile("release.json")
	if err != nil {
		t.Fatal(err)
	}
	var release struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &release); err != nil {
		t.Fatal(err)
	}
	if release.Version == "" {
		t.Fatal("release.json has no version")
	}
	if got := Version(); got != release.Version {
		t.Fatalf("Version() = %q, want %q from release.json", got, release.Version)
	}
}

func TestVersionFallsBackToUndefined(t *testing.T) {
	cases := []string{
		"",
		"{",
		`{}`,
		`{"version":""}`,
		`{"version":"  "}`,
		`{"version":2}`,
	}

	for _, raw := range cases {
		if got := versionFrom([]byte(raw)); got != "undefined" {
			t.Fatalf("versionFrom(%q) = %q, want undefined", raw, got)
		}
	}
}
