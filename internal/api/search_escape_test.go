package api

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bright-interaction/flare/internal/db/generated"
)

// Without escaping, a user typing "%" matched every issue and "_" matched any
// character, so the search box silently lied about what it found. The queries
// declare ESCAPE '\', so the backslash has to be escaped first or it swallows
// the character after it.
func TestEscapeLike(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "timeout", "timeout"},
		{"percent", "50%", `50\%`},
		{"underscore", "user_id", `user\_id`},
		{"backslash", `C:\tmp`, `C:\\tmp`},
		{"backslash before wildcard", `a\%b`, `a\\\%b`},
		{"all three", `%_\`, `\%\_\\`},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := escapeLike(tt.in); got != tt.want {
				t.Fatalf("escapeLike(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// The search parameter used to be byte-sliced at 200, the exact bug class this
// branch fixed in the issue-title truncation: a split multi-byte character
// becomes invalid UTF-8, which Postgres rejects, erroring the whole query.
func TestTruncateRunesKeepsValidUTF8(t *testing.T) {
	for _, in := range []string{
		strings.Repeat("a", 500),
		strings.Repeat("å", 300),
		strings.Repeat("🔥", 200),
		strings.Repeat("測", 150),
	} {
		got := truncateRunes(in, 200)
		if !utf8.ValidString(got) {
			t.Fatalf("truncateRunes produced invalid UTF-8 for %.20q: %q", in, got)
		}
		if len(got) > 200 {
			t.Fatalf("truncateRunes returned %d bytes, over the limit", len(got))
		}
	}
}

// Two rules of the same type used to collapse to whichever the query returned
// last, so a tighter threshold could silently never evaluate.
func TestMoreSensitiveRulePicksTheOneThatFiresFirst(t *testing.T) {
	tight := &generated.AlertRule{Threshold: 10, WindowMinutes: 5}
	loose := &generated.AlertRule{Threshold: 100, WindowMinutes: 5}
	if !moreSensitiveRule(tight, loose) {
		t.Error("a lower threshold must win")
	}
	if moreSensitiveRule(loose, tight) {
		t.Error("a higher threshold must not win")
	}

	// Equal thresholds: the longer window catches more, so it wins.
	wide := &generated.AlertRule{Threshold: 10, WindowMinutes: 60}
	narrow := &generated.AlertRule{Threshold: 10, WindowMinutes: 5}
	if !moreSensitiveRule(wide, narrow) {
		t.Error("at an equal threshold the longer window must win")
	}
	if moreSensitiveRule(narrow, wide) {
		t.Error("the shorter window must not win at an equal threshold")
	}
}
