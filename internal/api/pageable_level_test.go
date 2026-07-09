package api

import "testing"

func TestPageableLevel(t *testing.T) {
	cases := map[string]bool{
		"info":    false, // heartbeats/breadcrumbs never page
		"INFO":    false, // case-insensitive
		" debug ": false, // trimmed
		"debug":   false,
		"warning": true,
		"error":   true,
		"fatal":   true,
		"":        true, // default/empty == error per Sentry semantics -> pages
	}
	for level, want := range cases {
		if got := pageableLevel(level); got != want {
			t.Errorf("pageableLevel(%q) = %v, want %v", level, got, want)
		}
	}
}
