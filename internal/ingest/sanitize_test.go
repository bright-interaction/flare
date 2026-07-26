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

// SanitizeJSON must PARSE, not text-replace. The escape can itself be escaped,
// so {"k":"\\u0000"} is a valid document whose value is the literal six
// characters; a text replace leaves {"k":"\\"}, which is invalid.
func TestSanitizeJSONStaysValidJSON(t *testing.T) {
	tests := []string{
		`{"k":"v"}`,
		`{"k":"before\u0000after"}`,
		`{"nested":{"a":["\u0000",1,true,null]}}`,
		// The regression case: an ESCAPED backslash followed by u0000.
		`{"k":"\\u0000"}`,
		`{"win":"C:\\u0000path"}`,
		// Lone surrogate: Postgres rejects it in jsonb; the decoder folds it.
		`{"k":"\ud800"}`,
		`{"unicode":"åäö"}`,
		`[]`,
	}
	for _, in := range tests {
		out := SanitizeJSON([]byte(in))
		if !json.Valid(out) {
			t.Fatalf("SanitizeJSON(%s) = %s, which is not valid JSON", in, out)
		}
		if strings.IndexByte(string(out), 0) >= 0 {
			t.Fatalf("SanitizeJSON(%s) left a raw NUL byte: %q", in, out)
		}
	}
	if got := SanitizeJSON(nil); got != nil {
		t.Fatalf("SanitizeJSON(nil) = %v, want nil", got)
	}
}

// Guards against a SanitizeJSON that "passes" by returning {} for everything:
// clean input must survive semantically intact, including large integers,
// which must not be rounded through float64, and HTML characters, which must
// not be escaped into \u003c.
func TestSanitizeJSONPreservesCleanInput(t *testing.T) {
	in := `{"n":1712345678901234567,"s":"a <b> & c","f":1.5,"b":true,"z":null,"arr":[1,"x"]}`
	out := SanitizeJSON([]byte(in))
	if !strings.Contains(string(out), "1712345678901234567") {
		t.Fatalf("large integer lost precision: %s", out)
	}
	if !strings.Contains(string(out), "a <b> & c") {
		t.Fatalf("HTML characters were escaped: %s", out)
	}
	var want, got map[string]any
	if err := json.Unmarshal([]byte(in), &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output did not unmarshal: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("key count changed: got %d, want %d (%s)", len(got), len(want), out)
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Fatalf("key %q was dropped: %s", k, out)
		}
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
