package api

import (
	"testing"

	"github.com/bright-interaction/flare/internal/ingest"
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
