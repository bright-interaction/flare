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
