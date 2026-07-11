// Package ciguard is Flare's structural backstop against cross-tenant data
// leaks. It parses the SQL query source tree itself so a brand-new query that
// reads a tenant-scoped table without an org_id predicate fails CI, even
// though every existing query is correctly scoped. Mirrors a common
// source-parsing ciguard pattern.
//
// A query that legitimately reads a scoped table by an unguessable secret
// (an ingest public_key, an API-key hash) opts out with a trailing marker:
//
//	-- ciguard:allow-unscoped <reason>
//
// on the line directly above its `-- name:` directive.
package ciguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scopedTables hold per-tenant rows. Any read of these must filter on org_id
// (directly or via a join the guard can see) unless explicitly allowlisted.
var scopedTables = []string{
	"events", "logs", "spans", "issues",
	"projects", "api_keys", "alert_rules", "notification_channels", "monitors",
	"ai_triage_usage",
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 12; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("ciguard: could not locate go.mod")
	return ""
}

func TestQueriesAreTenantScoped(t *testing.T) {
	root := repoRoot(t)
	queriesDir := filepath.Join(root, "internal", "db", "queries")

	entries, err := os.ReadDir(queriesDir)
	if err != nil {
		t.Fatalf("read queries dir: %v", err)
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(queriesDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		checkFile(t, e.Name(), string(raw))
	}
}

func checkFile(t *testing.T, file, src string) {
	lines := strings.Split(src, "\n")
	var (
		name    string
		body    []string
		allowed bool
		flush   = func() {
			if name == "" {
				return
			}
			joined := strings.ToLower(strings.Join(body, " "))
			if allowed || !readsScopedTable(joined) {
				return
			}
			if !strings.Contains(joined, "org_id") {
				t.Errorf("%s: query %q reads a tenant-scoped table without an org_id predicate (add the filter or an `-- ciguard:allow-unscoped <reason>` marker)", file, name)
			}
		}
	)

	pendingAllow := false
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, "-- ciguard:allow-unscoped") {
			pendingAllow = true
			continue
		}
		if strings.HasPrefix(trimmed, "-- name:") {
			flush()
			name = trimmed
			body = nil
			allowed = pendingAllow
			pendingAllow = false
			continue
		}
		pendingAllow = false
		if name != "" {
			body = append(body, ln)
		}
	}
	flush()
}

func readsScopedTable(lowerBody string) bool {
	for _, tbl := range scopedTables {
		for _, kw := range []string{"from " + tbl, "join " + tbl, "update " + tbl, "into " + tbl, "delete from " + tbl} {
			if strings.Contains(lowerBody, kw) {
				return true
			}
		}
	}
	return false
}

// telemetryTables hold per-PROJECT rows (not just per-org). A trace_id is not
// project-unique, so org_id alone can mix projects (the cross-project trace bug
// the session-review caught). Any read of these must filter project_id too,
// unless it is a by-id lookup where the id is itself project-unique (marked
// with `-- ciguard:allow-no-project <reason>`).
var telemetryTables = []string{"events", "logs", "spans", "issues"}

func TestTelemetryReadsAreProjectScoped(t *testing.T) {
	root := repoRoot(t)
	queriesDir := filepath.Join(root, "internal", "db", "queries")
	entries, err := os.ReadDir(queriesDir)
	if err != nil {
		t.Fatalf("read queries dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(queriesDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		checkProjectScope(t, e.Name(), string(raw))
	}
}

func checkProjectScope(t *testing.T, file, src string) {
	lines := strings.Split(src, "\n")
	var (
		name    string
		body    []string
		allowed bool
		flush   = func() {
			if name == "" {
				return
			}
			joined := strings.ToLower(strings.Join(body, " "))
			if allowed || !readsTelemetryTable(joined) {
				return
			}
			if !strings.Contains(joined, "project_id") {
				t.Errorf("%s: query %q reads a telemetry table without a project_id predicate (add it or a `-- ciguard:allow-no-project <reason>` marker)", file, name)
			}
		}
	)

	pendingAllow := false
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, "-- ciguard:allow-no-project") {
			pendingAllow = true
			continue
		}
		if strings.HasPrefix(trimmed, "-- name:") {
			flush()
			name = trimmed
			body = nil
			allowed = pendingAllow
			pendingAllow = false
			continue
		}
		pendingAllow = false
		if name != "" {
			body = append(body, ln)
		}
	}
	flush()
}

func readsTelemetryTable(lowerBody string) bool {
	for _, tbl := range telemetryTables {
		for _, kw := range []string{"from " + tbl, "join " + tbl, "update " + tbl, "into " + tbl, "delete from " + tbl} {
			if strings.Contains(lowerBody, kw) {
				return true
			}
		}
	}
	return false
}
