package ratelimit

import (
	"strconv"
	"testing"
	"time"
)

// TestLiveLockoutSurvivesFlood reproduces the lockout-bypass the ceiling
// eviction used to allow. A live lockout counter (5 failed logins, unexpired)
// is created FIRST, so it carries the earliest reset in the map. An attacker
// then floods the shared limiter with non-lockout junk keys until the ceiling
// is reached and eviction fires. Because eviction drops the earliest-reset
// entries, the pre-fix code evicted the attacker's own lockout first, letting
// them resume past the 5-attempt cap.
//
// Against the unfixed evictOldest this fails: the lockout is gone and blockedAt
// returns false. With the exemption for unexpired at-limit entries it survives.
func TestLiveLockoutSurvivesFlood(t *testing.T) {
	if testing.Short() {
		t.Skip("fills the limiter to its ceiling")
	}
	l := New(5, 15*time.Minute)

	// Lockout is created first, at t0, so its reset is the earliest in the map.
	t0 := time.Unix(1_000_000, 0)
	lockKey := "login:victim@example.com|198.51.100.7"
	for i := 0; i < 5; i++ {
		l.recordAt(lockKey, t0)
	}
	if !l.blockedAt(lockKey, t0) {
		t.Fatal("precondition: key must be locked out after 5 failures")
	}

	// Junk flood arrives later, so every junk key has a LATER reset than the
	// lockout. Fill up to the ceiling, then push one more to trigger eviction.
	later := t0.Add(time.Minute)
	for i := 0; i < maxKeys-1; i++ {
		l.recordAt("junk"+strconv.Itoa(i), later)
	}
	if got := len(l.hits); got != maxKeys {
		t.Fatalf("precondition: want map at ceiling %d, got %d", maxKeys, got)
	}
	l.recordAt("junk-trigger", later) // len >= maxKeys on insert -> evictOldest

	// The lockout is unexpired at `later` (t0+15m is still ahead), so it must
	// still block. On the unfixed code it was evicted as the earliest reset.
	if !l.blockedAt(lockKey, later) {
		t.Fatal("live lockout was evicted by a junk flood: attacker can resume past the cap")
	}
}

// TestCeilingHoldsUnderNonLockoutFlood confirms the exemption does not defeat
// the ceiling for ordinary keys. A flood of distinct NON-lockout keys (each
// counted once, well below the limit) must never grow the map past maxKeys:
// none of them are exempt, so eviction keeps reclaiming them.
//
// This is the guard against writing the exemption too broadly. If the exempt
// predicate ever matched ordinary keys, eviction would free nothing and the map
// would grow without bound past maxKeys, which this asserts against.
func TestCeilingHoldsUnderNonLockoutFlood(t *testing.T) {
	if testing.Short() {
		t.Skip("floods well past the ceiling")
	}
	l := New(5, time.Minute)
	now := time.Unix(2_000_000, 0)

	// Push far past the ceiling with single-count keys (count 1 < limit 5).
	for i := 0; i < maxKeys+2*evictBatch+1000; i++ {
		l.recordAt("k"+strconv.Itoa(i), now)
		if got := len(l.hits); got > maxKeys {
			t.Fatalf("map exceeded ceiling: %d entries > maxKeys %d after inserting %d keys; "+
				"eviction is not reclaiming ordinary keys", got, maxKeys, i+1)
		}
	}
}

// TestCeilingHoldsWhenEveryEntryIsExempt is the case the test above misses, and
// the reason a "guard exists, so this is covered" reading is wrong.
//
// The exemption is `count >= limit && unexpired`, and its comment justifies the
// bound by arguing an attacker must EARN each exempt entry with a full at-limit
// run of requests. That argument silently assumes limit is meaningfully greater
// than 1. Flare runs a limiter where it is not: secIPLimiter is New(1, 10s), so
// the very first request for a key leaves it at its limit and therefore exempt.
//
// With every entry exempt, evictOldest finds nothing it may evict and returns
// having freed zero. maxKeys stops bounding anything, and worse, the O(n) scan
// and its allocation then repeat on EVERY subsequent insert instead of once per
// batch, under the one lock that serialises every key in that limiter.
//
// The test above cannot catch this: at limit 5 with count 1, no entry is exempt.
func TestCeilingHoldsWhenEveryEntryIsExempt(t *testing.T) {
	if testing.Short() {
		t.Skip("floods well past the ceiling")
	}
	// The exact shape of secIPLimiter: <=1 per (kind, ip) / 10s.
	l := New(1, 10*time.Second)
	now := time.Unix(3_000_000, 0)

	for i := 0; i < maxKeys+2*evictBatch+1000; i++ {
		l.recordAt("k"+strconv.Itoa(i), now)
		if got := len(l.hits); got > maxKeys {
			t.Fatalf("map exceeded ceiling: %d entries > maxKeys %d after inserting %d keys; "+
				"at limit=1 every entry is exempt, so eviction frees nothing and "+
				"maxKeys bounds nothing", got, maxKeys, i+1)
		}
	}
}

// TestEvictionStaysAmortizedWhenEveryEntryIsAtLimit is the cost half of the same
// defect, and the reason bounded memory alone is not enough.
//
// evictBatch exists so a ceiling eviction frees many entries at once and the
// next evictBatch inserts pay nothing. An eviction that frees ZERO breaks that:
// the ceiling is still met on the very next insert, so the O(n) scan and its
// ~4.8 MB allocation run again, per request, holding the lock the whole time.
// Measured before the fix at 2.28 ms and 4.8 MB per insert, unbounded in time
// because the map only grows.
//
// Asserting on evicts (work actually done) rather than on map size is
// deliberate: a fix that bounded memory while still scanning per request would
// pass the test above and leave the amplification in place.
func TestEvictionStaysAmortizedWhenEveryEntryIsAtLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("fills the limiter to its ceiling")
	}
	l := New(1, 10*time.Second)
	now := time.Unix(3_500_000, 0)
	for i := 0; i < maxKeys; i++ {
		l.recordAt("k"+strconv.Itoa(i), now)
	}

	before := l.evicts
	const extra = 3 * evictBatch
	for i := 0; i < extra; i++ {
		l.recordAt("extra"+strconv.Itoa(i), now)
	}

	// Each eviction frees a full batch, so the run costs one scan per batch of
	// inserts, plus one for the partial batch in flight.
	got := l.evicts - before
	if want := extra/evictBatch + 1; got > want {
		t.Errorf("%d O(n log n) evictions for %d inserts past the ceiling, want at most %d: "+
			"eviction is freeing nothing and repeating per request", got, extra, want)
	}
}
