package ratelimit

import (
	"strconv"
	"testing"
	"time"
)

// TestEvictOldestIsNotQuadratic pins the cost of the eviction path.
//
// The original implementation kept an n-element "oldest" slice and, for every
// remaining map entry, linearly scanned that slice to find its newest member.
// With maxKeys=200000 and evictBatch=20000 that is ~180,000 x 20,000 = 3.6
// BILLION comparisons, all under the global mutex. Measured at 6.45s of
// lock-held single-threaded work.
//
// Its comment justified the design as avoiding "a full sort of a 200k-entry
// map". That sort is ~200k*log2(200k) = ~3.6M operations: a thousand times
// cheaper than the loop written to avoid it. The optimisation cost three orders
// of magnitude more than the thing it optimised away.
//
// This matters because the eviction runs on the same hot path as the sweep that
// was rate-limited earlier: an unauthenticated client controls the ingest
// limiter's key, so it can hold the map at the ceiling and make every request
// pay. Rate-limiting the 2.76ms sweep while leaving a 6.45s eviction in place
// moved the wedge threshold without removing it.
//
// The budget is deliberately loose. It is not trying to pin a precise duration
// on unknown hardware; it is asserting the difference between milliseconds and
// seconds, which is the difference between a bounded cost and a denial of
// service.
func TestEvictOldestIsNotQuadratic(t *testing.T) {
	if testing.Short() {
		t.Skip("fills a 200k-entry map")
	}
	l := New(100, time.Minute)
	now := time.Now()
	for i := 0; i < maxKeys; i++ {
		l.hits["k"+strconv.Itoa(i)] = &entry{count: 1, reset: now.Add(time.Duration(i) * time.Millisecond)}
	}

	start := time.Now()
	l.evictOldest(evictBatch, now)
	elapsed := time.Since(start)

	const budget = 500 * time.Millisecond
	if elapsed > budget {
		t.Errorf("evictOldest took %s for one batch, budget %s. This runs under the single "+
			"global mutex on a path an unauthenticated caller controls, so every millisecond "+
			"here is lock contention every other request pays for.", elapsed, budget)
	}
	t.Logf("evictOldest(%d) over %d entries: %s", evictBatch, maxKeys, elapsed)

	// It must still actually evict, and evict the RIGHT ones: the entries
	// closest to expiry are the least useful to keep.
	if got := len(l.hits); got > maxKeys-evictBatch {
		t.Errorf("evicted too few: %d entries remain, want <= %d", got, maxKeys-evictBatch)
	}
	// The earliest reset (k0) should be gone; a late one should survive.
	if _, ok := l.hits["k0"]; ok {
		t.Error("k0 has the earliest reset and should have been evicted first")
	}
	if _, ok := l.hits["k"+strconv.Itoa(maxKeys-1)]; !ok {
		t.Error("the entry with the latest reset was evicted; eviction is picking the wrong end")
	}
}
