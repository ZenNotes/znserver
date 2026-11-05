package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSoftMemoryLimit(t *testing.T) {
	write := func(body string) string {
		path := filepath.Join(t.TempDir(), "limit")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	missing := filepath.Join(t.TempDir(), "absent")
	for name, tc := range map[string]struct {
		user  string
		files []string
		want  int64
	}{
		"a container's limit": {"", []string{write("1073741824\n")}, 751619276},
		"the user's wins":     {"2GiB", []string{write("1073741824\n")}, 0},
		"no limit, cgroup v2": {"", []string{write("max\n")}, 0},
		"no limit, cgroup v1": {"", []string{write("9223372036854771712\n")}, 0},
		"no file":             {"", []string{missing}, 0},
		"the second file":     {"", []string{missing, write("1000\n")}, 700},
		"nonsense":            {"", []string{write("lots")}, 0},
		"zero":                {"", []string{write("0")}, 0},
	} {
		if got := softMemoryLimit(tc.user, tc.files); got != tc.want {
			t.Errorf("%s: got %d, want %d", name, got, tc.want)
		}
	}
}

func TestServerBoundsHowLongHeadersMayTake(t *testing.T) {
	if got := newHTTPServer("127.0.0.1:0", nil).ReadHeaderTimeout; got <= 0 {
		t.Errorf("ReadHeaderTimeout = %s", got)
	}
}
