package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ZenNotes/znserver/internal/config"
)

const healthcheckTimeout = 3 * time.Second

// runHealthcheck is `zennotes-server healthcheck`. The Docker image holds this
// binary and nothing else, so no shell, wget or curl can run a Compose
// healthcheck inside it (#49). It asks the server already running in the
// container and exits 0 when /healthz answers, 1 when not.
func runHealthcheck() int {
	cfg := config.Load()
	if err := probeHealthz(healthzURL(cfg.Bind, cfg.BasePath), healthcheckTimeout); err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		return 1
	}
	return 0
}

// healthzURL is /healthz as seen from inside the container. A wildcard bind
// (0.0.0.0, [::], :7878) is a place to listen, not an address to call.
func healthzURL(bind, basePath string) string {
	host, port, err := net.SplitHostPort(bind)
	if err != nil {
		host, port = bind, "7878"
	}
	switch strings.Trim(host, "[]") {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	return "http://" + net.JoinHostPort(strings.Trim(host, "[]"), port) + basePath + "/healthz"
}

// probeHealthz ignores any HTTP proxy in the environment, which could answer
// for a server that is down, and does not follow redirects: only the server's
// own 200 counts.
func probeHealthz(url string, timeout time.Duration) error {
	client := &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d", url, resp.StatusCode)
	}
	return nil
}
