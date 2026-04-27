package main

import "testing"

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
