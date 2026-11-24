// Package cloudengine runs ZenNotes Cloud's own sync engine against this
// server: the Go half builds the server, the Node half (testdata/) drives
// CloudSyncCoordinator and DesktopCloudSyncRepository from a ZenNotes checkout
// against it. See testdata/engine.test.ts for the scenarios.
package cloudengine

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCloudSyncEngineAgainstTheServer needs ZENNOTES_SRC, a ZenNotes/zennotes
// checkout at the commit in testdata/zennotes-ref (CI clones it), and Node 22
// or later. It skips only when ZENNOTES_SRC is unset, so CI cannot skip it.
func TestCloudSyncEngineAgainstTheServer(t *testing.T) {
	src := strings.TrimSpace(os.Getenv("ZENNOTES_SRC"))
	if src == "" {
		t.Skip("set ZENNOTES_SRC to a ZenNotes/zennotes checkout at testdata/zennotes-ref")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("node is needed: %v", err)
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go is needed: %v", err)
	}
	if want, err := os.ReadFile(filepath.Join("testdata", "zennotes-ref")); err == nil {
		if out, err := exec.Command("git", "-C", src, "rev-parse", "HEAD").Output(); err == nil &&
			strings.TrimSpace(string(out)) != strings.TrimSpace(string(want)) {
			t.Logf("note: %s is at %s, testdata/zennotes-ref pins %s", src, strings.TrimSpace(string(out)), strings.TrimSpace(string(want)))
		}
	}

	binary := filepath.Join(t.TempDir(), "zennotes-server")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	run(t, "", nil, goTool, "build", "-o", binary, "github.com/ZenNotes/znserver/cmd/zennotes-server")

	testdata, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(testdata, "node_modules", "esbuild")); err != nil {
		npm, err := exec.LookPath("npm")
		if err != nil {
			t.Fatalf("run npm ci in %s first: %v", testdata, err)
		}
		run(t, testdata, nil, npm, "ci", "--no-audit", "--no-fund")
	}
	run(t, testdata, []string{"ZENNOTES_SRC=" + src}, node, "build.mjs")
	run(t, testdata, []string{"ZENNOTES_SERVER_BINARY=" + binary}, node, "--test", "--test-reporter=spec", filepath.Join("dist", "engine.test.mjs"))
}

func run(t *testing.T, dir string, env []string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	t.Logf("%s %s\n%s", filepath.Base(name), strings.Join(args, " "), out)
	if err != nil {
		t.Fatalf("%s %s: %v", filepath.Base(name), strings.Join(args, " "), err)
	}
}
