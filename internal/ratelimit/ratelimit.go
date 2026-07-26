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
// ingest limiter runs pre-auth on an attacker-supplied header) frees nothing and
// grows the map without bound, taking the single global mutex on every insert.
// Past this ceiling a key that has no entry yet is refused rather than
// allocated: fail closed, since only an abnormal flood can reach it.
const maxKeys = 4 * sweepThreshold

// get returns the live window for key, creating/rolling it when absent or
// expired. Returns nil when the map is at its hard ceiling and key is new.
// Caller holds the lock.
func (l *Limiter) get(key string, now time.Time) *entry {
	e := l.hits[key]
	if e != nil && now.Before(e.reset) {
		return e
	}
	if len(l.hits) > sweepThreshold {
		for k, v := range l.hits {
			if !now.Before(v.reset) {
				delete(l.hits, k)
			}
		}
	}
	if e == nil && len(l.hits) >= maxKeys {
		return nil
	}
	e = &entry{count: 0, reset: now.Add(l.window)}
	l.hits[key] = e
	return e
}

func (l *Limiter) allowAt(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.get(key, now)
	if e == nil {
		return false // at the hard key ceiling: refuse rather than allocate
	}
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
	if e := l.get(key, now); e != nil {
		e.count++
	}
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
