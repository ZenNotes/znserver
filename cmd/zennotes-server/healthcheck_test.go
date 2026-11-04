package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
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
		{"[::]:7878", "", "http://127.0.0.1:7878/healthz"},
		{"127.0.0.1:7878", "", "http://127.0.0.1:7878/healthz"},
		{"[::1]:7878", "", "http://[::1]:7878/healthz"},
		{"10.0.0.5:7878", "", "http://10.0.0.5:7878/healthz"},
		{"localhost:7878", "", "http://localhost:7878/healthz"},
		{"0.0.0.0:7878", "/zen", "http://127.0.0.1:7878/zen/healthz"},
		{"0.0.0.0:7878", "/my notes", "http://127.0.0.1:7878/my%20notes/healthz"},
		{"0.0.0.0:7878", "/100%", "http://127.0.0.1:7878/100%25/healthz"},
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
	for _, basePath := range []string{"/zen", "/my notes", "/100%"} {
		cfg := config.Config{AuthToken: "secret", BasePath: basePath}
		srv := httptest.NewServer(httpserver.New(nil, nil, nil, cfg).Router())
		url := healthzURL(strings.TrimPrefix(srv.URL, "http://"), basePath)
		err := probeHealthz(url, time.Second)
		srv.Close()
		if err != nil {
			t.Errorf("base path %q: %v", basePath, err)
		}
	}
}

// The wildcard case relies on a [::] listener also answering on IPv4.
func TestProbeHealthzReachesAnIPv6WildcardListener(t *testing.T) {
	ln, err := net.Listen("tcp", "[::]:0")
	if err != nil {
		t.Skipf("no IPv6 here: %v", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	defer srv.Close()
	if err := probeHealthz(healthzURL(ln.Addr().String(), ""), time.Second); err != nil {
		t.Fatal(err)
	}
}
