package api

import (
	"strings"
	"testing"
)

// TestMonitorSlugRe guards the check-in slug validation: it must accept the
// tokens a cron would use and reject path-breaking or oversized input, since
// the slug lands in a URL path segment and a stored row.
func TestMonitorSlugRe(t *testing.T) {
	ok := []string{"nightly-backup", "db_prune", "sync.hourly", "a", "Job-1", "x1234567890"}
	for _, s := range ok {
		if !monitorSlugRe.MatchString(s) {
			t.Errorf("expected slug %q to be valid", s)
		}
	}
	bad := []string{"", "-leading", ".dot-first", "has space", "slash/here", "semi;colon", "qmark?x", strings.Repeat("a", 65)}
	for _, s := range bad {
		if monitorSlugRe.MatchString(s) {
			t.Errorf("expected slug %q to be rejected", s)
		}
	}
}
