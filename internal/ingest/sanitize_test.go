package ingest

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// truncate used to slice on a raw byte boundary, producing invalid UTF-8 that
// Postgres rejects on insert, which silently dropped every non-ASCII
// message-only event whose text straddled the limit.
func TestTruncateNeverProducesInvalidUTF8(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
	}{
		{"ascii under limit", "hello", 200},
		{"ascii exactly at limit", strings.Repeat("a", 200), 200},
		{"swedish straddling boundary", strings.Repeat("å", 150), 200},
		{"emoji straddling boundary", strings.Repeat("🔥", 80), 200},
		{"cjk straddling boundary", strings.Repeat("測", 90), 200},
		{"cut inside a 4-byte rune", "aaa🔥", 5},
		{"limit zero", "🔥", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.in, tt.n)
			if !utf8.ValidString(got) {
				t.Fatalf("truncate(%q, %d) produced invalid UTF-8: %q", tt.in, tt.n, got)
			}
			if len(got) > tt.n {
				t.Fatalf("truncate(%q, %d) returned %d bytes, over the limit", tt.in, tt.n, len(got))
			}
		})
	}
}

// The exception form of a title was unbounded, so an 8 MiB exception message
// became an 8 MiB title that the issues list then returned 50 of per request.
func TestTitleIsAlwaysBounded(t *testing.T) {
	huge := strings.Repeat("x", 1<<20)
	for _, ev := range []NormalizedEvent{
		{ExceptionType: "Error", ExceptionValue: huge},
		{ExceptionType: huge},
		{Message: huge},
	} {
		if got := title(ev); len(got) > titleMax {
			t.Fatalf("title returned %d bytes, want <= %d", len(got), titleMax)
		}
	}
}

// Out-of-window client timestamps land in the DEFAULT partition, which
// retention never prunes, the exporter never archives, and which permanently
// blocks creating that day's partition.
func TestClampTime(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		in      time.Time
		wantNow bool
	}{
		{"zero value", time.Time{}, true},
		{"now", now, false},
		{"one hour ago", now.Add(-time.Hour), false},
		{"inside backfill window", now.AddDate(0, 0, -MaxBackfillDays+1), false},
		{"just past backfill window", now.AddDate(0, 0, -MaxBackfillDays).Add(-time.Hour), true},
		{"year 1970", time.Unix(0, 0).UTC(), true},
		{"far future", now.AddDate(50, 0, 0), true},
		{"inside skew allowance", now.Add(MaxAhead - time.Hour), false},
		{"past skew allowance", now.Add(MaxAhead + time.Hour), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClampTime(tt.in, now)
			if tt.wantNow && !got.Equal(now) {
				t.Fatalf("ClampTime(%v) = %v, want clamped to now (%v)", tt.in, got, now)
			}
			if !tt.wantNow && !got.Equal(tt.in) {
				t.Fatalf("ClampTime(%v) = %v, want unchanged", tt.in, got)
			}
		})
	}
}

// Logs, spans and metrics are written with CopyFrom, which is all-or-nothing:
// one NUL byte or invalid UTF-8 in one record failed the ENTIRE batch, and an
// OTLP collector retrying that batch made it a permanent poison pill.
func TestSanitizeText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"clean", "all good", "all good"},
		{"embedded NUL", "before\x00after", "beforeafter"},
		{"only NUL", "\x00", ""},
		{"valid multibyte preserved", "hej så mycket 🔥", "hej så mycket 🔥"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SanitizeText(tt.in); got != tt.want {
				t.Fatalf("SanitizeText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}

	// Invalid UTF-8 must not survive, whatever it is replaced with.
	if got := SanitizeText("bad\xff\xfebytes"); !utf8.ValidString(got) {
		t.Fatalf("SanitizeText left invalid UTF-8: %q", got)
	}
}

// Postgres rejects the \u0000 escape inside a jsonb value, so an attribute
// carrying one poisons the batch exactly like a raw NUL byte. These use Go raw
// string literals, so the six characters \ u 0 0 0 0 reach the function as-is.
func TestSanitizeJSONStaysValidJSON(t *testing.T) {
	tests := []string{
		`{"k":"v"}`,
		`{"k":"before\u0000after"}`,
		`{"nested":{"a":["\u0000",1,true,null]}}`,
		`{"unicode":"åäö"}`,
		`[]`,
	}
	for _, in := range tests {
		out := SanitizeJSON([]byte(in))
		if !json.Valid(out) {
			t.Fatalf("SanitizeJSON(%s) = %s, which is not valid JSON", in, out)
		}
		if strings.Contains(string(out), `\u0000`) {
			t.Fatalf("SanitizeJSON(%s) left a raw NUL escape: %s", in, out)
		}
		if strings.IndexByte(string(out), 0) >= 0 {
			t.Fatalf("SanitizeJSON(%s) left a raw NUL byte: %q", in, out)
		}
	}
	if got := SanitizeJSON(nil); got != nil {
		t.Fatalf("SanitizeJSON(nil) = %v, want nil", got)
	}
}

// The partition manager pre-creates partitions from behindDays back to
// aheadDays forward. Ingest must never accept a timestamp outside what it
// pre-creates, or the row lands in DEFAULT anyway.
func TestBackfillWindowMatchesPartitionPrecreation(t *testing.T) {
	// partition.behindDays is 7; keep this assertion in lockstep with it.
	const partitionBehindDays = 7
	if MaxBackfillDays > partitionBehindDays {
		t.Fatalf("MaxBackfillDays (%d) exceeds the partition manager's behindDays (%d): accepted timestamps would have no partition",
			MaxBackfillDays, partitionBehindDays)
	}
	// aheadDays is 3 in the partition manager; MaxAhead must stay inside it.
	if MaxAhead >= 3*24*time.Hour {
		t.Fatalf("MaxAhead (%v) reaches the partition manager's 3-day pre-creation horizon", MaxAhead)
	}
}
