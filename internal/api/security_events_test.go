package api

import (
	"testing"

	"github.com/bright-interaction/flare/internal/ingest"
	"github.com/bright-interaction/flare/internal/ratelimit"
)

func TestBuildSecurityEventGroupsByKind(t *testing.T) {
	a, err := ingest.ParseEvent(buildSecurityEventJSON("ingest-auth-rejected", "unknown key ab12 from 1.2.3.4"))
	if err != nil {
		t.Fatal(err)
	}
	if a.ExceptionType != "SecurityEvent" || a.ExceptionValue != "ingest-auth-rejected" {
		t.Fatalf("type/value = %q/%q", a.ExceptionType, a.ExceptionValue)
	}
	if a.Level != "warning" {
		t.Fatalf("level = %q, want warning", a.Level)
	}

	// Same kind, different detail -> SAME fingerprint (one issue, count rises).
	b, _ := ingest.ParseEvent(buildSecurityEventJSON("ingest-auth-rejected", "unknown key cd34 from 5.6.7.8"))
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("same kind should share a fingerprint")
	}
	// Different kind -> DIFFERENT fingerprint (distinct issue).
	c, _ := ingest.ParseEvent(buildSecurityEventJSON("login-lockout", "x"))
	if a.Fingerprint() == c.Fingerprint() {
		t.Fatal("different kinds must not share a fingerprint")
	}
}

func TestLoginLockoutTripsAfterBudget(t *testing.T) {
	// The login handler fires the "login-lockout" security event exactly when
	// loginLimiter.Blocked becomes true after Record. Prove that boundary so
	// the trigger can't silently regress (the recording path itself is covered
	// live + by TestBuildSecurityEventGroupsByKind).
	lim := ratelimit.New(loginFailBudget, loginFailWindow)
	key := "login:attacker@evil.test|1.2.3.4"
	for i := 0; i < loginFailBudget-1; i++ {
		lim.Record(key)
		if lim.Blocked(key) {
			t.Fatalf("blocked after only %d failed logins, want %d", i+1, loginFailBudget)
		}
	}
	lim.Record(key) // the loginFailBudget-th failure
	if !lim.Blocked(key) {
		t.Fatalf("not blocked after %d failed logins; login-lockout event would never fire", loginFailBudget)
	}
}
