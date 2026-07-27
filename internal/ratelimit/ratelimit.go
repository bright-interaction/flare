// Package ratelimit is a small in-memory, keyed fixed-window limiter.
//
// It needs no Redis, so a single-binary self-host works out of the box. When
// scaled to multiple nodes each node enforces its own window - a looser but
// safe degradation. Stale keys are swept opportunistically so a flood of unique
// keys (IPs, DSN keys) cannot itself exhaust memory.
package ratelimit

import (
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
		l.evictOldest(evictBatch)
	}
	e = &entry{count: 0, reset: now.Add(l.window)}
	l.hits[key] = e
	return e
}

// evictOldest removes up to n entries with the earliest reset time. Caller
// holds the lock.
func (l *Limiter) evictOldest(n int) {
	type kv struct {
		key   string
		reset time.Time
	}
	oldest := make([]kv, 0, n)
	for k, v := range l.hits {
		if len(oldest) < n {
			oldest = append(oldest, kv{k, v.reset})
			continue
		}
		// Replace the newest entry currently held, so the slice converges on
		// the n earliest resets without a full sort of a 200k-entry map.
		worst := 0
		for i := 1; i < len(oldest); i++ {
			if oldest[i].reset.After(oldest[worst].reset) {
				worst = i
			}
		}
		if v.reset.Before(oldest[worst].reset) {
			oldest[worst] = kv{k, v.reset}
		}
	}
	for _, o := range oldest {
		delete(l.hits, o.key)
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
