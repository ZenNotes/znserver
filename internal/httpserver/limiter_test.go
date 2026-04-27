package httpserver

import (
	"testing"
	"time"
)

func TestBackoffDelay(t *testing.T) {
	cases := []struct {
		failures int
		want     time.Duration
	}{
		{0, 0},
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 16 * time.Second},
		{6, 32 * time.Second},
		{7, 60 * time.Second}, // capped
		{99, 60 * time.Second},
	}
	for _, c := range cases {
		if got := backoffDelay(c.failures); got != c.want {
			t.Errorf("backoffDelay(%d) = %v, want %v", c.failures, got, c.want)
		}
	}
}

// TestAttemptLimiterRejectsImmediateRetry is the integration check for
// the backoff machinery. The first call goes through; an immediate
// second call is rejected because the inter-attempt minimum hasn't
// elapsed.
func TestAttemptLimiterRejectsImmediateRetry(t *testing.T) {
	l := newAttemptLimiter(10*time.Minute, 100)
	if !l.allow("k") {
		t.Fatal("first allow should pass")
	}
	if l.allow("k") {
		t.Fatal("immediate second allow should be rejected by backoff")
	}
}

func TestAttemptLimiterResetClearsBackoff(t *testing.T) {
	l := newAttemptLimiter(10*time.Minute, 100)
	if !l.allow("k") {
		t.Fatal("first allow should pass")
	}
	l.reset("k")
	if !l.allow("k") {
		t.Fatal("after reset, allow should pass again")
	}
}

func TestAttemptLimiterIndependentKeys(t *testing.T) {
	l := newAttemptLimiter(10*time.Minute, 100)
	if !l.allow("alice") {
		t.Fatal("alice first allow should pass")
	}
	if !l.allow("bob") {
		t.Fatal("bob's allow should pass independently of alice")
	}
}
