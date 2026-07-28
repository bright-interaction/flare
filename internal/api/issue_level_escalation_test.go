package api

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestIssueLevelNeverDowngrades guards the severity of an open issue.
//
// UpsertIssue used to set `level = EXCLUDED.level`, so the newest event decided
// the issue's severity outright. Ingest authenticates with a DSN public key that
// ships inside a browser bundle, so anyone who can load a customer's page can
// post an event carrying the same fingerprint and level='info'. pageableLevel
// treats info and debug as never-page, so a single request switches off alerting
// for a live incident, and nothing in the UI reports that the level moved.
//
// Verified against the live schema before the fix landed: two events under one
// fingerprint, the second at 'info', left the issue at level 'info'. After the
// fix the same sequence leaves it at 'error' with event_count 2, so the event is
// still recorded and only the downgrade is refused.
//
// This is a source assertion because the behaviour lives in SQL that no Go unit
// test executes; the query is only exercised against a real Postgres.
func TestIssueLevelNeverDowngrades(t *testing.T) {
	root := repoRootForQueries(t)
	body, err := os.ReadFile(filepath.Join(root, "internal", "db", "queries", "events.sql"))
	if err != nil {
		t.Fatalf("read events.sql: %v", err)
	}
	q := upsertIssueBody(t, string(body))

	// Strip line comments BEFORE matching. The comment explaining this fix quotes
	// the defective form verbatim ("this used to be level = EXCLUDED.level"), so
	// a naive match fires on the documentation of the fix rather than on code.
	// ciguard learned the same lesson; a guard that reads comments is grading
	// prose. Cheap to get wrong, and it fails in the direction that looks like a
	// real finding, which wastes the next person's time.
	q = regexp.MustCompile(`--[^\n]*`).ReplaceAllString(q, "")

	// The bare assignment is the defect. Anything that sets level unconditionally
	// from the incoming event hands severity to whoever sent it.
	bare := regexp.MustCompile(`(?i)\blevel\s*=\s*EXCLUDED\.level\b`)
	if bare.MatchString(q) {
		t.Error("UpsertIssue sets level = EXCLUDED.level unconditionally. Any DSN holder can then " +
			"downgrade a live incident to 'info', which pageableLevel treats as never-page, silently " +
			"disabling alerts. Level must escalate only.")
	}
	if !strings.Contains(strings.ToLower(q), "array_position") || !strings.Contains(strings.ToUpper(q), "CASE") {
		t.Error("UpsertIssue no longer ranks levels; escalate-only requires comparing the incoming " +
			"level's rank against the stored one")
	}
	// Empty/unknown must rank as 'error' to match pageableLevel's documented
	// "empty == error per Sentry semantics"; otherwise an event with no level
	// would rank lowest and could never escalate anything.
	if !strings.Contains(q, "NULLIF") {
		t.Error("UpsertIssue does not normalise an empty level; empty must rank as 'error' to match pageableLevel")
	}

	// Title and culprit are first-write-wins for the same reason level escalates
	// only: a DSN public key ships in a browser bundle, so a later event must not
	// be able to rename an existing issue. Verified against the live schema:
	// posting "ATTACKER CONTROLLED TITLE" on an existing fingerprint leaves the
	// title as "DatabaseError: connection refused" and only increments the count.
	for _, col := range []string{"title", "culprit"} {
		bareCol := regexp.MustCompile(`(?i)\b` + col + `\s*=\s*EXCLUDED\.` + col + `\b`)
		if bareCol.MatchString(q) {
			t.Errorf("UpsertIssue sets %s = EXCLUDED.%s, so any DSN holder can rename an existing "+
				"issue. It stays open and alerting while reading as something else in the list, in "+
				"alert bodies and in the BYOAI triage prompt. Keep it first-write-wins.", col, col)
		}
	}
}

func upsertIssueBody(t *testing.T, src string) string {
	t.Helper()
	i := strings.Index(src, "-- name: UpsertIssue")
	if i < 0 {
		t.Fatal("UpsertIssue not found in events.sql")
	}
	rest := src[i+1:]
	if j := strings.Index(rest, "-- name: "); j >= 0 {
		return rest[:j]
	}
	return rest
}

func repoRootForQueries(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 12; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("go.mod not found walking up from the test directory")
	return ""
}
