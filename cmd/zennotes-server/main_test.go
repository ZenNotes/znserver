package main

import (
	"testing"

	"github.com/ZenNotes/znserver/internal/config"
)

func TestBindIsLoopbackTreatsEmptyHostAsNonLoopback(t *testing.T) {
	cases := []struct {
		bind string
		want bool
	}{
		{":7878", false},
		{"0.0.0.0:7878", false},
		{"[::]:7878", false},
		{"127.0.0.1:7878", true},
		{"[::1]:7878", true},
		{"localhost:7878", true},
	}

	for _, tc := range cases {
		if got := bindIsLoopback(tc.bind); got != tc.want {
			t.Fatalf("bindIsLoopback(%q) = %v, want %v", tc.bind, got, tc.want)
		}
	}
}

// The banner names the rules the server applies, and a loopback bind adds none.
func TestCorsModeTellsTheTruth(t *testing.T) {
	cases := []struct {
		cfg  config.Config
		want string
	}{
		{config.Config{Bind: "127.0.0.1:7878"}, "same-origin only (set ZENNOTES_ALLOWED_ORIGINS for browser or WebView clients)"},
		{config.Config{Bind: "0.0.0.0:7878"}, "same-origin only (set ZENNOTES_ALLOWED_ORIGINS for browser or WebView clients)"},
		{config.Config{Bind: "127.0.0.1:7878", DevMode: true}, "same-origin plus any loopback origin with credentials (ZENNOTES_DEV)"},
		{config.Config{AllowedOrigins: []string{"https://a.example"}}, "same-origin plus https://a.example"},
		{config.Config{AllowedOrigins: []string{"https://a.example"}, DevMode: true}, "same-origin plus https://a.example plus any loopback origin with credentials (ZENNOTES_DEV)"},
		{config.Config{AllowedOrigins: []string{"*"}}, "any origin (ZENNOTES_ALLOWED_ORIGINS=*; credentials withheld cross-origin)"},
		{config.Config{AllowedOrigins: []string{"*"}, DevMode: true}, "any origin (ZENNOTES_ALLOWED_ORIGINS=*; credentials withheld cross-origin) plus any loopback origin with credentials (ZENNOTES_DEV)"},
	}
	for _, tc := range cases {
		if got := corsMode(tc.cfg); got != tc.want {
			t.Errorf("corsMode(%+v) = %q, want %q", tc.cfg, got, tc.want)
		}
	}
}

func TestHostsModeNamesTheHostCheck(t *testing.T) {
	cases := []struct {
		cfg  config.Config
		want string
	}{
		{config.Config{Bind: "127.0.0.1:7878"}, "localhost and IP addresses only (no auth token; set ZENNOTES_ALLOWED_HOSTS to add a name)"},
		{config.Config{Bind: "127.0.0.1:7878", AllowedHosts: []string{"notes.example.com"}}, "localhost and IP addresses plus notes.example.com"},
		{config.Config{Bind: "127.0.0.1:7878", AuthToken: "t"}, ""},
		{config.Config{Bind: "0.0.0.0:7878"}, ""},
	}
	for _, tc := range cases {
		if got := hostsMode(tc.cfg); got != tc.want {
			t.Errorf("hostsMode(%+v) = %q, want %q", tc.cfg, got, tc.want)
		}
	}
}
