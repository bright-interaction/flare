package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The ingest rate limiter runs BEFORE authentication and buckets on this value,
// so an unbounded key let an anonymous caller mint unlimited distinct limiter
// entries and grow the limiter map without bound. Implausible values must
// resolve to "" so the request falls back to the IP bucket instead.
func TestPlausibleIngestKey(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"typical cuid", "m90byhmwg8tlyjhb3ieja46n", "m90byhmwg8tlyjhb3ieja46n"},
		{"with dash and underscore", "abc-DEF_123", "abc-DEF_123"},
		{"empty", "", ""},
		{"at the length limit", strings.Repeat("a", maxIngestKeyLen), strings.Repeat("a", maxIngestKeyLen)},
		{"one over the limit", strings.Repeat("a", maxIngestKeyLen+1), ""},
		{"megabyte header", strings.Repeat("a", 1<<20), ""},
		{"spaces", "abc def", ""},
		{"punctuation", "abc;drop", ""},
		{"newline injection", "abc\ndef", ""},
		{"unicode", "nyckelå", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := plausibleIngestKey(tt.in); got != tt.want {
				t.Fatalf("plausibleIngestKey(%.40q) = %.40q, want %.40q", tt.in, got, tt.want)
			}
		})
	}
}

// Every transport that carries a DSN key must go through the same shape check.
func TestIngestKeyBoundsEveryTransport(t *testing.T) {
	huge := strings.Repeat("z", 1<<16)

	t.Run("X-Sentry-Auth", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/api/p/store", nil)
		r.Header.Set("X-Sentry-Auth", "Sentry sentry_key="+huge+", sentry_version=7")
		if got := ingestKey(r); got != "" {
			t.Fatalf("oversized sentry_key accepted (%d bytes)", len(got))
		}
	})

	t.Run("query param", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/api/p/store?sentry_key="+huge, nil)
		if got := ingestKey(r); got != "" {
			t.Fatalf("oversized query key accepted (%d bytes)", len(got))
		}
	})

	t.Run("X-Flare-Key", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/api/p/logs", nil)
		r.Header.Set("X-Flare-Key", huge)
		if got := ingestKey(r); got != "" {
			t.Fatalf("oversized flare key accepted (%d bytes)", len(got))
		}
	})

	t.Run("a real key still resolves", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/api/p/store", nil)
		r.Header.Set("X-Sentry-Auth", "Sentry sentry_key=m90byhmwg8tlyjhb3ieja46n, sentry_version=7")
		if got := ingestKey(r); got != "m90byhmwg8tlyjhb3ieja46n" {
			t.Fatalf("valid key = %q, want it to resolve", got)
		}
	})
}
