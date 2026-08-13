package api

import (
	"strings"
	"testing"
)

// The limiter key must be fixed-width whatever the caller sends, because it is
// retained for the full window before any auth or DB work happens.
func TestLimiterEmailKeyIsBounded(t *testing.T) {
	cases := []struct {
		name  string
		email string
	}{
		{"normal", "a@b.com"},
		{"empty", ""},
		{"1MB ascii", strings.Repeat("a", 1<<20) + "@x.com"},
		{"400k multibyte", strings.Repeat("é", 400000) + "@x.com"},
		{"8MB", strings.Repeat("z", 8<<20)},
	}
	const want = 32 // hex of 16 bytes
	for _, c := range cases {
		if got := len(limiterEmailKey(c.email)); got != want {
			t.Errorf("%s: key is %d bytes, want exactly %d (input was %d bytes)",
				c.name, got, want, len(c.email))
		}
	}
}

// Distinct emails must stay in distinct buckets. Truncating instead of hashing
// would collapse every long address into one key, so one attacker's traffic
// would trip the lockout on an unrelated user's login.
func TestLimiterEmailKeySeparatesDistinctEmails(t *testing.T) {
	long := strings.Repeat("a", 1<<20)
	a := limiterEmailKey(long + "one@x.com")
	b := limiterEmailKey(long + "two@x.com")
	if a == b {
		t.Errorf("two distinct long emails collapsed into one bucket: %s", a)
	}
	if limiterEmailKey("user@x.com") == limiterEmailKey("other@x.com") {
		t.Error("two ordinary emails collapsed into one bucket")
	}
	// Same input must be stable, or the limiter never trips.
	if limiterEmailKey("user@x.com") != limiterEmailKey("user@x.com") {
		t.Error("key is not stable across calls")
	}
}

// The retention property the CRITICAL was about: N distinct oversized emails
// must cost O(N) fixed-width keys, not O(N * payload).
func TestLimiterKeyRetentionIsIndependentOfInputSize(t *testing.T) {
	big := strings.Repeat("a", 1<<20)
	var total int
	for i := 0; i < 2000; i++ {
		total += len(limiterEmailKey(big + string(rune('a'+i%26)) + "@x.com"))
	}
	// 2000 keys must cost ~64 KB, not the ~1.9 GiB the raw emails cost.
	if total > 200_000 {
		t.Errorf("2000 keys retain %d bytes, want under 200000", total)
	}
}
