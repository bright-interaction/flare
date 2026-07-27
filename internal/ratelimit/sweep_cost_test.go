package ratelimit

import (
	"strconv"
	"testing"
	"time"
)

// TestSweepDoesNotRunOnEveryInsert measures the amplification this fix removes.
//
// The ingest limiter runs PRE-AUTH on a caller-supplied header, so an
// unauthenticated client controls the key and can make every request a map
// miss. The reclaim used to run on every miss once the map passed
// sweepThreshold, and it walks the whole map under the single global mutex. So
// one cheap request bought a 50k-200k entry scan, serialized against every other
// request in the process. That is a far better deal for the attacker than simply
// sending more requests.
//
// The sweep can only reclaim EXPIRED windows, and nothing expires in under one
// window, so running it more than once per second cannot free anything the
// previous run did not. Rate-limiting it by time keeps the amortized cost at one
// scan per second at any request rate.
//
// The test asserts on observed work rather than on the clock: it counts how many
// distinct keys survive, which is only possible if the sweep is being skipped.
func TestSweepDoesNotRunOnEveryInsert(t *testing.T) {
	l := New(100, time.Minute)

	// Fill past the sweep threshold with live (unexpired) entries, the state an
	// attacker establishes in one window.
	base := time.Now()
	for i := 0; i < sweepThreshold+1000; i++ {
		l.get("k"+strconv.Itoa(i), base)
	}
	if got := len(l.hits); got < sweepThreshold {
		t.Fatalf("setup failed: %d entries, want > %d", got, sweepThreshold)
	}

	// Every one of these is a miss on a live map. Before the fix each would walk
	// the entire map. They all share one instant, so at most ONE sweep may run.
	l.sweeps = 0
	const flood = 5000
	for i := 0; i < flood; i++ {
		l.get("flood"+strconv.Itoa(i), base)
	}

	// Assert on WORK DONE, not on bookkeeping. The first version of this test
	// checked lastSweep, which the pre-fix code never wrote, so it passed against
	// exactly the code it existed to reject.
	if l.sweeps > 1 {
		t.Errorf("%d full-map sweeps for %d requests at one instant, want at most 1. "+
			"Each sweep walks the whole map under the global mutex, so an unauthenticated "+
			"flood of unique keys costs the server O(n) per request.", l.sweeps, flood)
	}

	// A later instant is allowed to sweep again, otherwise expired windows would
	// accumulate forever and the ceiling would be doing all the work.
	later := base.Add(2 * time.Minute)
	before := len(l.hits)
	l.get("after-expiry", later)
	if len(l.hits) >= before {
		t.Errorf("no reclaim happened after the interval elapsed: %d entries before, %d after; "+
			"expired windows must still be swept or memory only ever shrinks by eviction", before, len(l.hits))
	}
}

// TestSweepStillBoundsMemory keeps the fix honest in the other direction. Making
// the sweep rarer must not let the map grow without bound; maxKeys plus
// evictOldest is what enforces that, and this proves the two work together.
func TestSweepStillBoundsMemory(t *testing.T) {
	l := New(100, time.Minute)
	now := time.Now()
	for i := 0; i < maxKeys+evictBatch+5000; i++ {
		l.get("k"+strconv.Itoa(i), now)
	}
	if len(l.hits) > maxKeys {
		t.Fatalf("map grew past the ceiling: %d entries, max %d", len(l.hits), maxKeys)
	}
}
