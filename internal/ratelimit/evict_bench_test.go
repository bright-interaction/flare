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
// HOW this is asserted matters as much as what.
//
// The first version of this test measured one eviction and compared it to a
// fixed 500ms budget. That is a statement about the machine, not about the
// algorithm, and it went red on 2026-07-30 while two mirror publishes were
// building on the same runner. It passed on an idle one. Worse than the noise:
// ci-go stops at the first failing directory, so this flake sat in front of a
// real data race in hephaestus and a broken test in sentinel and hid both for
// as long as it had been failing.
//
// So assert the SHAPE of the cost instead of its magnitude. Double the entry
// count and the batch together and measure the ratio. Quadratic work goes up
// about 4x; the sort-based implementation goes up about 2.1x. A threshold of 3
// sits in the gap and does not care how fast the machine is, because both
// measurements come from the same machine in the same run.
func TestEvictOldestIsNotQuadratic(t *testing.T) {
	if testing.Short() {
		t.Skip("fills a 200k-entry map")
	}

	small := measureEvict(maxKeys / 2)
	large := measureEvict(maxKeys)
	t.Logf("evict at n=%d: %s; at n=%d: %s", maxKeys/2, small, maxKeys, large)

	// A measurement too small to resolve makes the ratio meaningless: at
	// microsecond scale the timer granularity dominates and the test would
	// report noise as a complexity regression.
	if small < 100*time.Microsecond {
		t.Skipf("baseline %s is below timer resolution for a meaningful ratio", small)
	}

	const maxRatio = 3.0
	if ratio := float64(large) / float64(small); ratio > maxRatio {
		t.Errorf("doubling n took %.1fx longer (%s -> %s), want under %.1fx. "+
			"Quadratic eviction is ~4x per doubling. This runs under the single global "+
			"mutex on a path an unauthenticated caller controls, so the cost here is lock "+
			"contention every other request pays for.", ratio, small, large, maxRatio)
	}

	// It must still actually evict, and evict the RIGHT ones: the entries
	// closest to expiry are the least useful to keep.
	l := filledLimiter(maxKeys)
	l.evictOldest(evictBatch, time.Now())
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

// filledLimiter builds a limiter holding n entries whose reset times ascend with
// the key index, so "oldest" is unambiguous and k0 is always the first to go.
func filledLimiter(n int) *Limiter {
	l := New(100, time.Minute)
	now := time.Now()
	for i := 0; i < n; i++ {
		l.hits["k"+strconv.Itoa(i)] = &entry{count: 1, reset: now.Add(time.Duration(i) * time.Millisecond)}
	}
	return l
}

// measureEvict returns the FASTEST of several evictions of n/10 entries from a
// map of n. Minimum rather than mean on purpose: scheduling noise, GC and a busy
// neighbour only ever ADD time, so the fastest sample is both the closest
// estimate of the real cost and the one least disturbed by whatever else the
// runner is doing. Taking the mean would import exactly the flakiness this test
// was rewritten to remove.
func measureEvict(n int) time.Duration {
	const samples = 3
	best := time.Duration(0)
	for i := 0; i < samples; i++ {
		l := filledLimiter(n)
		now := time.Now()
		start := time.Now()
		l.evictOldest(n/10, now)
		d := time.Since(start)
		if best == 0 || d < best {
			best = d
		}
	}
	return best
}
