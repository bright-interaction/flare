package api

import (
	"os"
	"strings"
	"testing"
)

// TestWatchdogCountsDifferBySeverity locks in the fix for the false "Error-rate
// anomaly" storm.
//
// Both rule types used to share one count (CountEventsForProjectSince), which
// counts EVERY level. So a project that went from silent to emitting the
// info-level "service-up" heartbeat looked like an infinite error spike against
// its own zero baseline. The entire estate tripped this on 2026-07-11, the day
// the heartbeat shipped (flare, atomicsite, brightcrm, dockyard,
// scanner-dashboard-go, svar-go all fired), and mesh-hub + mesh-cloud tripped it
// again the moment they were first instrumented.
//
// The two counts must stay different:
//
//	anomaly  -> actionable only. Heartbeats are liveness, not errors.
//	silence  -> everything. The heartbeat is exactly what proves a quiet,
//	            error-free service is still alive, so filtering it out would make
//	            every healthy service look dead.
func TestWatchdogCountsDifferBySeverity(t *testing.T) {
	raw, err := os.ReadFile("../db/queries/watchdog.sql")
	if err != nil {
		t.Fatalf("read watchdog.sql: %v", err)
	}
	sql := string(raw)

	anomaly := queryBody(t, sql, "CountActionableEventsForProjectSince")
	if !strings.Contains(anomaly, "level NOT IN") {
		t.Error("the ANOMALY count must exclude info/debug, or a heartbeat fires an error-rate alert")
	}

	silence := queryBody(t, sql, "CountEventsForProjectSince")
	if strings.Contains(silence, "level NOT IN") {
		t.Error("the SILENCE count must NOT exclude info/debug: the heartbeat IS the liveness signal")
	}
}

// queryBody returns the SQL of a single named sqlc query.
func queryBody(t *testing.T, sql, name string) string {
	t.Helper()
	marker := "-- name: " + name + " :"
	i := strings.Index(sql, marker)
	if i < 0 {
		t.Fatalf("query %q not found in watchdog.sql", name)
	}
	rest := sql[i+len(marker):]
	end := strings.Index(rest, ";")
	if end < 0 {
		t.Fatalf("query %q has no terminating semicolon", name)
	}
	return rest[:end]
}

// TestHeartbeatOnlyProjectNeverFiresAnomaly is the production scenario itself: a
// healthy service emitting nothing but heartbeats has ZERO actionable events, so
// the anomaly rule must stay quiet however loud the beacon is, while still firing
// on a genuine error spike.
func TestHeartbeatOnlyProjectNeverFiresAnomaly(t *testing.T) {
	// Actionable counts for a heartbeat-only service: 0 recent, 0 over 24h.
	if got := anomalyReason(0, 0, 60, 3); got != "" {
		t.Errorf("anomaly fired on a heartbeat-only project: %q", got)
	}
	// A real spike must still fire: 30 errors in the window vs a quiet day.
	if got := anomalyReason(30, 35, 60, 3); got == "" {
		t.Error("anomaly must still fire on a genuine error spike")
	}
}
