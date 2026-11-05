package znserver

import (
	"encoding/json"
	"os"
	"strings"
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

// A scratch image has no /tmp unless it ships one, and an upload over 8 MiB needs it.
func TestImageShipsATempDirectory(t *testing.T) {
	raw, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"chmod 1777 /out/root/tmp", "COPY --from=build /out/root/ /"} {
		if !strings.Contains(text, want) {
			t.Errorf("the Dockerfile lacks %q", want)
		}
	}
}

// Without a toolchain line, a build uses exactly the go line's release, and misses fixes.
func TestGoModNamesAToolchain(t *testing.T) {
	raw, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "\ntoolchain go1.26.") {
		t.Error("go.mod has no toolchain line")
	}
}
