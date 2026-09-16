package vault

import (
	"os"
	"testing"
)

// ListNotes schedules a note-meta cache write one second later. Tests remove
// their temporary vaults as soon as they finish, and on Windows that write
// raced the removal ("The directory is not empty"). Keep the writer off for
// this package's tests; a test that opts back in must drain it with Close.
func TestMain(m *testing.M) {
	if os.Getenv("ZEN_PERF_DISABLE_PERSISTED_META_CACHE") == "" {
		_ = os.Setenv("ZEN_PERF_DISABLE_PERSISTED_META_CACHE", "1")
	}
	os.Exit(m.Run())
}
