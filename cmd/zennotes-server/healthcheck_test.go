package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ZenNotes/znserver/internal/config"
	"github.com/ZenNotes/znserver/internal/httpserver"
)

func TestHealthzURLCallsAnAddressNotAWildcard(t *testing.T) {
	cases := []struct {
		bind, basePath, want string
	}{
		{"0.0.0.0:7878", "", "http://127.0.0.1:7878/healthz"},
		{":9000", "", "http://127.0.0.1:9000/healthz"},
		{"[::]:7878", "", "http://[::1]:7878/healthz"},
		{"127.0.0.1:7878", "", "http://127.0.0.1:7878/healthz"},
		{"[::1]:7878", "", "http://[::1]:7878/healthz"},
		{"10.0.0.5:7878", "", "http://10.0.0.5:7878/healthz"},
		{"localhost:7878", "", "http://localhost:7878/healthz"},
		{"0.0.0.0:7878", "/zen", "http://127.0.0.1:7878/zen/healthz"},
	}
	for _, tc := range cases {
		if got := healthzURL(tc.bind, tc.basePath); got != tc.want {
			t.Errorf("healthzURL(%q, %q) = %q, want %q", tc.bind, tc.basePath, got, tc.want)
		}
	}
}

func TestProbeHealthzPassesOnlyOnTheServersOwn200(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status == http.StatusFound {
			http.Redirect(w, r, "/elsewhere", status)
			return
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()

	if err := probeHealthz(srv.URL, time.Second); err != nil {
		t.Fatalf("200: %v", err)
	}
	for _, status = range []int{http.StatusInternalServerError, http.StatusUnauthorized, http.StatusFound} {
		if err := probeHealthz(srv.URL, time.Second); err == nil {
			t.Errorf("status %d passed", status)
		}
	}
}

func TestProbeHealthzFailsWhenNothingListens(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	if err := probeHealthz(url, time.Second); err == nil {
		t.Fatal("a closed port passed")
	}
}

func TestProbeHealthzGivesUpOnAHungServer(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	if err := probeHealthz(srv.URL, 50*time.Millisecond); err == nil {
		t.Fatal("a hung server passed")
	}
}

// The real router, with a token set and a base path: /healthz has to stay open,
// or every authenticated deployment would read as unhealthy.
func TestProbeHealthzReachesTheRealRouterWithoutAToken(t *testing.T) {
	cfg := config.Config{AuthToken: "secret", BasePath: "/zen"}
	srv := httptest.NewServer(httpserver.New(nil, nil, nil, cfg).Router())
	defer srv.Close()
	if err := probeHealthz(srv.URL+"/zen/healthz", time.Second); err != nil {
		t.Fatalf("/healthz behind auth or the base path: %v", err)
	}
}
