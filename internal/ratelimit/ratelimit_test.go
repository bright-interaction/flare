package ratelimit

import (
	"testing"
	"time"
)

func TestAllowFixedWindow(t *testing.T) {
	l := New(3, time.Minute)
	now := time.Unix(1000, 0)
	for i := 0; i < 3; i++ {
		if !l.allowAt("k", now) {
			t.Fatalf("event %d should be allowed", i)
		}
	}
	if l.allowAt("k", now) {
		t.Fatal("4th event should be denied")
	}
	// Next window: allowed again.
	if !l.allowAt("k", now.Add(time.Minute+time.Second)) {
		t.Fatal("event in new window should be allowed")
	}
}

func TestBlockedRecordReset(t *testing.T) {
	l := New(5, 15*time.Minute)
	now := time.Unix(2000, 0)
	key := "login:a@b.com|1.2.3.4"
	if l.blockedAt(key, now) {
		t.Fatal("fresh key must not be blocked")
	}
	for i := 0; i < 5; i++ {
		l.recordAt(key, now)
	}
	if !l.blockedAt(key, now) {
		t.Fatal("key must be blocked after 5 failures")
	}
	// Success clears the lockout.
	l.Reset(key)
	if l.blockedAt(key, now) {
		t.Fatal("Reset must clear the lockout")
	}
}

func TestBlockedWindowExpiry(t *testing.T) {
	l := New(5, 15*time.Minute)
	now := time.Unix(3000, 0)
	for i := 0; i < 5; i++ {
		l.recordAt("k", now)
	}
	if !l.blockedAt("k", now) {
		t.Fatal("should be blocked")
	}
	if l.blockedAt("k", now.Add(16*time.Minute)) {
		t.Fatal("lockout must lapse after the window")
	}
}

func TestKeysIsolated(t *testing.T) {
	l := New(1, time.Minute)
	now := time.Unix(4000, 0)
	if !l.allowAt("a", now) || !l.allowAt("b", now) {
		t.Fatal("distinct keys must not share a budget")
	}
	if l.allowAt("a", now) {
		t.Fatal("key a should now be limited")
	}
}
