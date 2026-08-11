package api

import (
	"testing"
	"time"

	"github.com/bright-interaction/flare/internal/ingest"
	"github.com/bright-interaction/flare/internal/telemetry"
)

// M1. Three guards were applied to the issue search term and none to the log
// search term: LIKE escaping, the 200-byte rune-safe cap, and SanitizeText.
// They travel together now or not at all.
func TestSearchTermAppliesEveryGuard(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"whitespace only", "   ", ""},
		{"like metacharacters", "100%", `100\%`},
		{"underscore wildcard", "user_id", `user\_id`},
		{"backslash first", `a\b`, `a\\b`},
		{"nul byte", "boom\x00", "boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := searchTerm(tt.in)
			if tt.want == "" {
				if got != nil {
					t.Fatalf("searchTerm(%q) = %q, want nil", tt.in, *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("searchTerm(%q) = nil, want %q", tt.in, tt.want)
			}
			if *got != tt.want {
				t.Fatalf("searchTerm(%q) = %q, want %q", tt.in, *got, tt.want)
			}
		})
	}
	// The cap is rune-safe: a byte slice through a multi-byte character hands
	// Postgres invalid UTF-8 and errors the whole query.
	long := ""
	for len(long) < 5000 {
		long += "ä"
	}
	got := searchTerm(long)
	if got == nil || len(*got) > maxSearchTermBytes {
		t.Fatalf("search term was not capped: len=%d", len(*got))
	}
	for _, r := range *got {
		if r == '�' {
			t.Fatal("the cap split a multi-byte rune")
		}
	}
}

// M1, third harm. `hours` was clamped and `since` was not, so
// ?since=0001-01-01T00:00:00Z with ?q=%% was a full unindexed ILIKE scan of the
// entire retained logs table, from any viewer session or read-only API key.
func TestLogWindowHasAFloor(t *testing.T) {
	ancient := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	f := telemetry.LogFilter{Since: &ancient}
	clampLogWindow(&f)
	if f.Since.Equal(ancient) {
		t.Fatal("an epoch-zero `since` was accepted, which is the unbounded scan")
	}
	if time.Since(*f.Since) > (maxLogWindowHours+1)*time.Hour {
		t.Fatalf("floor is %v old, want at most %d hours", time.Since(*f.Since), maxLogWindowHours)
	}

	// An absent window is the same scan, so it gets the floor too.
	var none telemetry.LogFilter
	clampLogWindow(&none)
	if none.Since == nil {
		t.Fatal("an absent `since` was left unbounded")
	}

	// A window inside the floor is left exactly as the caller asked.
	recent := time.Now().Add(-2 * time.Hour)
	inside := telemetry.LogFilter{Since: &recent}
	clampLogWindow(&inside)
	if !inside.Since.Equal(recent) {
		t.Fatalf("a valid window was rewritten: %v -> %v", recent, *inside.Since)
	}
}

// H5. The Go alert gate lowercased and trimmed; ten SQL predicates compared
// raw. So "INFO", which is what an OTLP-conventional service emits, was
// informational to the pager and actionable to every counter, which is the
// 2026-07-11 estate-wide page reproduced on demand by anyone with a DSN key.
func TestLevelIsCanonicalBeforeItIsStored(t *testing.T) {
	tests := []struct{ wire, want string }{
		{`"INFO"`, "info"},
		{`"  Debug  "`, "debug"},
		{`"WARNING"`, "warning"},
		{`""`, "error"},
	}
	for _, tt := range tests {
		ev, err := ingest.ParseEvent([]byte(`{"event_id":"a","level":` + tt.wire + `}`))
		if err != nil {
			t.Fatalf("ParseEvent(level=%s): %v", tt.wire, err)
		}
		if ev.Level != tt.want {
			t.Errorf("level %s stored as %q, want %q", tt.wire, ev.Level, tt.want)
		}
		// The stored value and the pager gate now read the same row the same
		// way, which is the property that was broken.
		if pageableLevel(ev.Level) != pageableLevel(tt.want) {
			t.Errorf("level %s: pager gate disagrees with the canonical form", tt.wire)
		}
	}
}
