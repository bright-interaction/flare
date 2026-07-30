// Package ratelimit is a small in-memory, keyed fixed-window limiter.
//
// It needs no Redis, so a single-binary self-host works out of the box. When
// scaled to multiple nodes each node enforces its own window - a looser but
// safe degradation. Stale keys are swept opportunistically so a flood of unique
// keys (IPs, DSN keys) cannot itself exhaust memory.
package ratelimit

import (
	"slices"
	"sync"
	"time"
)

type entry struct {
	count int
	reset time.Time
}

// Limiter is safe for concurrent use.
type Limiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[string]*entry
	// lastSweep rate-limits the O(n) reclaim. Without it the sweep ran on every
	// insert once the map passed sweepThreshold, which turned a flood of unique
	// keys into an amplification attack against the limiter itself: each request
	// is a map miss, each miss scans 50k-200k entries, and all of it is
	// serialized on mu. The attacker pays one request; the server pays a full
	// scan under the global lock.
	lastSweep time.Time
	// sweeps counts completed O(n) reclaims. It exists so a test can assert on
	// the WORK DONE rather than on bookkeeping: the first version of that test
	// checked lastSweep, which the pre-fix code never wrote, so it passed against
	// the very code it was meant to reject.
	sweeps int
}

// New returns a limiter allowing limit events per window per key.
func New(limit int, window time.Duration) *Limiter {
	return &Limiter{limit: limit, window: window, hits: make(map[string]*entry)}
}

// sweepThreshold bounds the map: once it grows past this, an allocation sweeps
// expired keys. High enough that normal traffic never triggers it.
const sweepThreshold = 50000

// maxKeys is a HARD ceiling on live entries. The sweep alone only reclaims
// EXPIRED windows, so a flood of unique keys arriving inside one window (the
// ingest limiter runs pre-auth on a caller-supplied header) frees nothing and
// grows the map without bound, taking the single global mutex on every insert.
//
// At the ceiling we EVICT rather than refuse. Refusing changes the meaning of
// the limiter depending on which method asked: Allow would deny a legitimate
// password reset, and Record would fail to tally a login failure, so a flood
// would make the brute-force lockout fail OPEN. Eviction keeps memory bounded
// without changing any caller's semantics.
const maxKeys = 4 * sweepThreshold

// evictBatch is how many entries are dropped once the ceiling is reached, so
// eviction is amortized instead of running on every subsequent insert.
const evictBatch = maxKeys / 10

// sweepInterval is the floor between two O(n) reclaims.
//
// The sweep only ever reclaims EXPIRED windows, and a window cannot expire in
// less than one window's duration, so sweeping more often than that cannot free
// anything that the previous sweep did not already free. Running it on every
// insert was therefore pure cost: the same scan, repeated, under the lock.
//
// One second is well below any window in use (ingest is a minute) and bounds
// the amortized cost at one scan per second regardless of request rate.
const sweepInterval = time.Second

// get returns the live window for key, creating or rolling it when absent or
// expired. Caller holds the lock. Never returns nil.
func (l *Limiter) get(key string, now time.Time) *entry {
	e := l.hits[key]
	if e != nil && now.Before(e.reset) {
		return e
	}
	if len(l.hits) > sweepThreshold && now.Sub(l.lastSweep) >= sweepInterval {
		l.lastSweep = now
		l.sweeps++
		for k, v := range l.hits {
			if !now.Before(v.reset) {
				delete(l.hits, k)
			}
		}
	}
	// Still at the ceiling after reclaiming expired windows: evict the entries
	// closest to expiry, which are the least useful to keep.
	if e == nil && len(l.hits) >= maxKeys {
		l.evictOldest(evictBatch, now)
	}
	e = &entry{count: 0, reset: now.Add(l.window)}
	l.hits[key] = e
	return e
}

// evictOldest removes up to n entries with the earliest reset time. Caller
// holds the lock.
//
// This used to keep an n-element "oldest" slice and, for every remaining map
// entry, linearly scan that slice to find its newest member. Its comment
// justified that as avoiding "a full sort of a 200k-entry map". The arithmetic
// runs the other way: with maxKeys=200000 and evictBatch=20000 the scan is
// ~180,000 x 20,000 = 3.6 BILLION comparisons, measured at 6.12s of lock-held
// single-threaded work, while the sort it avoided is ~3.6M operations. The
// optimisation cost a thousand times more than the thing it optimised away.
//
// That is not a micro-benchmark curiosity. Eviction runs on a path an
// unauthenticated caller controls (the ingest limiter keys on a request
// header), under the single global mutex shared with login and password reset.
// Six seconds of lock-held work per batch is a denial of service with a
// one-request price tag.
//
// Sorting the reset times and deleting below a threshold is O(n log n) and, on
// the same 200k map, milliseconds. Ties at the threshold may take slightly more
// than n entries, which is harmless: these are the entries closest to expiry
// and the limiter re-creates any key on its next request.
//
// An UNEXPIRED at-limit entry (count >= limit, reset still in the future) is a
// live lockout and is EXEMPT from eviction. Without this a flood of junk keys
// evicts the attacker's own auth-lockout counter: it was created first, so it
// carries the earliest reset and this scan would pick it before any of the
// later junk, letting the attacker resume past the cap. The exemption cannot
// grow the map without bound because a live lockout self-expires at its reset,
// and creating one costs the attacker the full at-limit run of requests per key
// (five failed logins, each paying a bcrypt, for the login limiter), so exempt
// entries can only accumulate as fast as they can be earned and drain as the
// window passes. Expired at-limit entries are NOT exempt: they are dead weight
// and evict like any other stale key.
func (l *Limiter) evictOldest(n int, now time.Time) {
	if n <= 0 || len(l.hits) == 0 {
		return
	}
	exempt := func(v *entry) bool { return v.count >= l.limit && now.Before(v.reset) }
	resets := make([]time.Time, 0, len(l.hits))
	for _, v := range l.hits {
		if exempt(v) {
			continue
		}
		resets = append(resets, v.reset)
	}
	if len(resets) == 0 {
		return
	}
	if n > len(resets) {
		n = len(resets)
	}
	slices.SortFunc(resets, func(a, b time.Time) int { return a.Compare(b) })
	threshold := resets[n-1]
	for k, v := range l.hits {
		if exempt(v) {
			continue
		}
		if !v.reset.After(threshold) {
			delete(l.hits, k)
		}
	}
}

func (l *Limiter) allowAt(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.get(key, now)
	if e.count >= l.limit {
		return false
	}
	e.count++
	return true
}

func (l *Limiter) blockedAt(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.hits[key]
	return e != nil && now.Before(e.reset) && e.count >= l.limit
}

func (l *Limiter) recordAt(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.get(key, now).count++
}

// Allow counts one event and reports whether key is still within its limit.
func (l *Limiter) Allow(key string) bool { return l.allowAt(key, time.Now()) }

// Blocked reports whether key has already hit its limit this window, without
// counting an event. Used to gate an action before doing work.
func (l *Limiter) Blocked(key string) bool { return l.blockedAt(key, time.Now()) }

// Record counts one event against key without a limit check. Used to tally
// failures (e.g. bad logins) that should eventually trip Blocked.
func (l *Limiter) Record(key string) { l.recordAt(key, time.Now()) }

// Reset clears key, e.g. after a successful login.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, key)
}
