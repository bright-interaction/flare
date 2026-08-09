package ratelimit

import (
	"math"
	"runtime"
	"runtime/debug"
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
// So assert the SHAPE of the cost instead of its magnitude, by measuring two
// sizes on the same machine in the same run and comparing them.
//
// The first attempt at that DOUBLED n and required the ratio to stay under 3.0,
// reasoning that quadratic work grows ~4x per doubling and the sort-based
// implementation ~2.1x, so 3.0 sits in the gap. It went red anyway on 2026-08-08
// at 3.03x, while publish-mesh-mirror built on the same runner.
//
// The threshold was not the problem; the step size was. Measured on an idle
// laptop, the real eviction's own ratio wandered between 1.32x and 3.57x per
// doubling. This is a Go map of 200k entries: the cost is memory latency, and it
// moves with cache state and allocator behaviour. So the measurement noise was
// about as large as the whole 2.1x-to-4x window the test was trying to resolve,
// and no threshold placed inside that window can be reliable.
//
// QUADRUPLE n instead. The separation grows much faster than the noise does:
//
//	               2x step        4x step
//	n log n        ~2.1x          ~4.5x
//	quadratic      ~4x            ~16x
//
// In a normal build, measured across idle and 8-way-loaded runs, the real
// eviction lands in 3.32x-6.47x and a deliberately quadratic workload in
// 18.36x-52.26x. Both sit above their theoretical values because of the
// estimator (see quietestRatio), which is fine: they shift together, and the gap
// between them is what matters. Under -race both shift again, and much further;
// see maxGrowthRatio, which is where the threshold lives.
func TestEvictOldestIsNotQuadratic(t *testing.T) {
	if testing.Short() {
		t.Skip("fills a 200k-entry map")
	}

	ratio, small, large := quietestEvictRatio(maxKeys/4, maxKeys)
	t.Logf("evict at n=%d: %s; at n=%d: %s (ratio %.2fx)",
		maxKeys/4, small, maxKeys, large, ratio)

	// A measurement too small to resolve makes the ratio meaningless: at
	// microsecond scale the timer granularity dominates and the test would
	// report noise as a complexity regression.
	if small < 100*time.Microsecond {
		t.Skipf("baseline %s is below timer resolution for a meaningful ratio", small)
	}

	if ratio > maxGrowthRatio() {
		t.Errorf("quadrupling n took %.1fx longer (%s -> %s), want under %.1fx. "+
			"Quadratic eviction is ~16x per quadrupling, the sort-based one ~4.5x. This "+
			"runs under the single global mutex on a path an unauthenticated caller "+
			"controls, so the cost here is lock contention every other request pays "+
			"for.", ratio, small, large, maxGrowthRatio())
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

// maxGrowthRatio is the largest quadrupling ratio the eviction path may show
// before this suite calls it a complexity regression. It is TWO numbers, because
// the race detector changes what is being measured.
//
// ci-go runs `go test -race`, and -race instruments every memory access through
// shadow memory whose own cost grows with the working set. At n=50000 the real
// eviction measures ~72ms instrumented against ~6.5ms uninstrumented, but at
// n=200000 it measures ~1.06s against ~30ms. So the SAME sort-based algorithm
// reports ~4.6x per quadrupling in a normal build and ~13x under -race. The
// detector manufactures precisely the superlinear signal this test exists to
// detect.
//
// That is the real reason this test kept going red, and it is why the 2026-08-08
// failure at 3.03x-against-3.0 looked like runner load and was not: every number
// in that CI log was an instrumented number compared against a threshold picked
// from uninstrumented reasoning. A busy runner made it worse, but the bias was
// already there and permanent.
//
// Skipping under -race would be the tidier answer and the wrong one: ci-go always
// passes -race, so the guard would never run anywhere that matters. Instead,
// calibrate per build. Measured over 10 runs per mode, idle and under an 8-way
// CPU load, real eviction against the deliberately quadratic stand-in:
//
//	            real             quadratic        threshold
//	normal      3.32x-6.47x      18.36x-52.26x    10
//	-race       8.65x-19.31x     43.59x-82.12x    30
//
// Each threshold clears the worst real reading by ~1.5x and sits ~1.5x under the
// best quadratic one. Load pushes BOTH sides up, which is why the binding
// constraint on the upper bound is an idle quadratic run, not a loaded one.
// TestQuietestEvictRatioCatchesQuadraticGrowth re-proves the lower bound in
// whichever build it runs in, so a threshold that drifts out of its gap fails
// there rather than silently disarming this test.
func maxGrowthRatio() float64 {
	if raceDetectorEnabled {
		return 30.0
	}
	return 10.0
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

// measureEvict times ONE eviction of n/10 entries from a map of n, with the
// garbage collector quiesced around the clock. quietestRatio does the sampling;
// this function's whole job is to make a single sample as honest as possible.
//
// Quiescing matters because the sampling cannot compensate for it. A minimum can
// only filter interference that varies BETWEEN samples. On 2026-08-08 CI measured
// a ratio of 3.03 against a 3.0 threshold while publish-mesh-mirror built on the
// same runner, and an IDLE laptop still produced minimums ranging 12.8ms to
// 19.1ms at n=100000: a 1.5x spread in a statistic meant to be the quiet floor.
//
// Every sample builds an n-entry map and then sorts inside the timed region, so
// every sample carries the same garbage-collection risk, and the collection it
// provokes lands inside the clock in all of them. That is a floor no minimum can
// see under. It also biases the RATIO rather than just adding noise, because the
// larger case allocates proportionally more and so trips the collector
// disproportionately: the numerator inflates while the denominator does not.
//
// So quiesce the collector around the measurement instead of averaging over it.
// runtime.GC() clears the garbage from building the map before the clock starts,
// and SetGCPercent(-1) guarantees no collection can begin inside the timed
// region. Both calls stop the world OUTSIDE the measured window, which is the
// point. The extra live heap is one sorted slice of n keys, briefly.
func measureEvict(n int) time.Duration {
	l := filledLimiter(n)
	now := time.Now()

	runtime.GC()
	prevGC := debug.SetGCPercent(-1)
	start := time.Now()
	l.evictOldest(n/10, now)
	d := time.Since(start)
	debug.SetGCPercent(prevGC)

	return d
}

// quietestEvictRatio reduces each size to its fastest observed eviction and
// returns large/small. See quietestRatio for why it is built the way it is, and
// TestQuietestEvictRatioCatchesQuadraticGrowth for the proof that it still
// refuses a genuinely quadratic implementation.
func quietestEvictRatio(nSmall, nLarge int) (ratio float64, small, large time.Duration) {
	return quietestRatio(measureEvict, nSmall, nLarge)
}

// quietestRatio is the estimator itself, taking the measurement function so the
// guard below can point it at a workload with known complexity.
func quietestRatio(measure func(int) time.Duration, nSmall, nLarge int) (ratio float64, small, large time.Duration) {
	const rounds = 5
	for i := 0; i < rounds; i++ {
		// INTERLEAVED, so both sizes are sampled across the same stretch of wall
		// clock. Measuring every small to completion first and only then the larges
		// lets a busy window overlapping just the second half inflate the ratio, and
		// that is a bias rather than noise: it can only push toward failure.
		if s := measure(nSmall); small == 0 || s < small {
			small = s
		}
		if l := measure(nLarge); large == 0 || l < large {
			large = l
		}
	}
	// INDEPENDENT minima, not the best single pair. Taking min(large/small) over
	// pairs looks equivalent and is not: one round whose SMALL sample was stalled
	// yields a small ratio from a large denominator, and the minimum then selects
	// exactly that round. Measured under an 8-way CPU load, the paired form
	// reported 1.98x for a workload that is genuinely ~16x, because one small
	// sample took 64ms instead of 11ms. Reducing each side on its own discards that
	// outlier instead of preferring it.
	return float64(large) / float64(small), small, large
}

// TestQuietestEvictRatioCatchesQuadraticGrowth is the guard on the guard.
//
// quietestRatio takes a MINIMUM, which biases toward passing. That bias is
// deliberate (see its comment) but it has a failure mode worth pinning: bias it
// far enough and the estimator stops discriminating, and then
// TestEvictOldestIsNotQuadratic keeps passing while the eviction path regresses
// to the quadratic implementation it exists to refuse. A guard that cannot fail
// is not a guard, and this test suite has shipped that mistake before.
//
// So run the same estimator against a workload that IS quadratic and require it
// to report growth the real threshold would reject.
func TestQuietestEvictRatioCatchesQuadraticGrowth(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a deliberately quadratic workload")
	}

	// Same 4x step either way, sized for the build. Instrumentation makes this
	// workload roughly 25x more expensive, so the pair that costs about a second
	// normally costs ~19s under -race; a quarter of the work brings that back to
	// ~5s without changing the verdict. Measured ratios: 18.36x-52.26x for
	// 12500 -> 50000 uninstrumented against a threshold of 10, and 43.59x-82.12x
	// for 6250 -> 25000 instrumented against a threshold of 30.
	nSmall, nLarge := 12500, 50000
	if raceDetectorEnabled {
		nSmall, nLarge = 6250, 25000
	}
	ratio, small, large := quietestRatio(quadraticEvict, nSmall, nLarge)
	t.Logf("quadratic stand-in at n=%d: %s; at n=%d: %s (ratio %.2fx, threshold %.1fx)",
		nSmall, small, nLarge, large, ratio, maxGrowthRatio())

	if ratio <= maxGrowthRatio() {
		t.Errorf("the estimator reported %.2fx for a deliberately quadratic workload, "+
			"which is at or under the %.1fx threshold TestEvictOldestIsNotQuadratic uses. "+
			"It has stopped discriminating, so that test would now pass a real "+
			"regression. Either the estimator has been biased low, or the threshold has "+
			"been raised out of its gap.", ratio, maxGrowthRatio())
	}
}

// quadraticEvict reproduces the SHAPE of the implementation the guard refuses:
// for every entry to evict, linearly scan all remaining entries to find the
// oldest. That is O(batch*n), so doubling n quadruples the work.
//
// It is a stand-in rather than the original code because the original ran at
// maxKeys=200000 and took 6.45s; the same shape at a fraction of that size makes
// the point in milliseconds. Quiesced identically to measureEvict so the two are
// comparable.
func quadraticEvict(n int) time.Duration {
	vals := make([]int64, n)
	for i := range vals {
		vals[i] = int64(n - i)
	}
	batch := n / 10

	runtime.GC()
	prevGC := debug.SetGCPercent(-1)
	start := time.Now()
	for b := 0; b < batch; b++ {
		oldest, idx := int64(math.MaxInt64), -1
		for i, v := range vals {
			if v >= 0 && v < oldest {
				oldest, idx = v, i
			}
		}
		if idx >= 0 {
			vals[idx] = -1
		}
	}
	d := time.Since(start)
	debug.SetGCPercent(prevGC)

	return d
}
