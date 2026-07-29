package ciguard

// Structural backstop for finding D3 (2026-07-29 audit).
//
// Migration 025 originally added a CHECK to the PARTITIONED metrics parent with
// a plain ADD CONSTRAINT. On a partitioned parent that takes AccessExclusive on
// the parent AND every child and scans every row before it commits, and these
// migrations run in-process at server boot, so it is a metric-ingest outage for
// the scan's whole duration and a single pre-existing bad row rolls back the
// entire deploy. The safe form is ADD CONSTRAINT ... NOT VALID (metadata-only,
// no scan, still enforced on new writes) followed by a separate VALIDATE
// CONSTRAINT (migration 029) that scans under ShareUpdateExclusive without
// blocking ingest.
//
// This guard fails CI if any migration after 021 adds a scan-bearing CHECK or
// FOREIGN KEY constraint to a partitioned parent WITHOUT NOT VALID, so the next
// person who reaches for ALTER TABLE <partitioned parent> ADD CONSTRAINT is
// stopped at the same trap rather than reintroducing the outage.

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var (
	// A CREATE TABLE whose definition is followed by PARTITION BY. The parent is
	// the table that owns children and takes the wide lock; PARTITION OF child
	// tables are deliberately not matched here.
	reCreatePartitioned = regexp.MustCompile(`(?is)CREATE TABLE\s+(?:IF NOT EXISTS\s+)?(\w+)\b[^;]*?\bPARTITION BY\b`)
	// One ALTER TABLE ... ; statement. The capture is the target table, matched
	// through the same spellings the tenant guard already defends against
	// (schema qualifier, ONLY, quotes are normalized away first).
	reAlterStmt = regexp.MustCompile(`(?is)\bALTER TABLE\s+(?:ONLY\s+)?(?:\w+\.)?(\w+)\b(.*?);`)
	reMigVer    = regexp.MustCompile(`^(\d+)_`)
)

// unsafeAddConstraints returns a human-readable reason for every ALTER TABLE
// statement in sql that adds a scan-bearing constraint (CHECK or FOREIGN KEY,
// the two that support NOT VALID) to a table in partitioned WITHOUT NOT VALID.
//
// sql must already have line comments stripped: migration 025's own comment now
// contains the words "ADD CONSTRAINT" describing the trap it avoids, and a guard
// that matched inside comments would flag the very migration that fixed the bug.
func unsafeAddConstraints(sql string, partitioned map[string]bool) []string {
	var out []string
	for _, m := range reAlterStmt.FindAllStringSubmatch(sql, -1) {
		table := strings.ToLower(m[1])
		if !partitioned[table] {
			continue
		}
		stmt := m[0]
		upper := strings.ToUpper(stmt)
		if !strings.Contains(upper, "ADD CONSTRAINT") {
			continue
		}
		// Only CHECK and FOREIGN KEY constraints scan existing rows and support
		// NOT VALID. UNIQUE / PRIMARY KEY build an index instead and have no
		// NOT VALID escape hatch, so flagging them would be unactionable noise.
		if !strings.Contains(upper, "CHECK") && !strings.Contains(upper, "REFERENCES") && !strings.Contains(upper, "FOREIGN KEY") {
			continue
		}
		if strings.Contains(upper, "NOT VALID") {
			continue
		}
		out = append(out, "ALTER TABLE "+table+" adds a scan-bearing constraint without NOT VALID")
	}
	return out
}

// stripLineComments removes -- comments so the scan sees only executable SQL.
// reLineComment is defined in guard_tenant_scoping_test.go.
func stripLineComments(sql string) string {
	return reLineComment.ReplaceAllString(sql, "")
}

// TestNoPlainAddConstraintOnPartitionedParent walks the real migrations.
func TestNoPlainAddConstraintOnPartitionedParent(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "db", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}

	// Discover partitioned parents across ALL migrations first: the parent may
	// be defined in an early file (events/logs/spans in 002) and altered in a
	// later one.
	partitioned := map[string]bool{}
	var files []os.DirEntry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		files = append(files, e)
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, m := range reCreatePartitioned.FindAllStringSubmatch(stripLineComments(string(b)), -1) {
			partitioned[strings.ToLower(m[1])] = true
		}
	}
	if len(partitioned) == 0 {
		t.Fatal("ciguard: discovered NO partitioned parents; the parser is broken, which would make this guard silently vacuous")
	}
	// The four known partitioned parents. If any disappears from this set the
	// parser regressed, so pin them.
	for _, want := range []string{"events", "logs", "spans", "metrics"} {
		if !partitioned[want] {
			t.Fatalf("ciguard: expected %q to be discovered as a partitioned parent; parser regressed", want)
		}
	}

	// Only migrations after 021 are in scope: everything up to and including the
	// metrics table (021) is already deployed and immutable, and none of them
	// add a constraint to a partitioned parent anyway.
	for _, e := range files {
		mm := reMigVer.FindStringSubmatch(e.Name())
		if mm == nil {
			continue
		}
		ver, _ := strconv.Atoi(mm[1])
		if ver <= 21 {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		sql := normalizeSQL(stripLineComments(string(b)))
		for _, reason := range unsafeAddConstraints(sql, partitioned) {
			t.Errorf("%s: %s. Use ADD CONSTRAINT ... NOT VALID plus a separate VALIDATE CONSTRAINT migration (see 025 + 029), or it locks the parent and every child AccessExclusive and scans every row at boot.", e.Name(), reason)
		}
	}
}

// TestUnsafeAddConstraintDetection is the negative control: it plants a real
// violation and asserts the detector refuses it, then confirms the two safe
// shapes pass. Without this, TestNoPlainAddConstraintOnPartitionedParent
// passing tells us nothing, because a detector that never fires also passes.
func TestUnsafeAddConstraintDetection(t *testing.T) {
	partitioned := map[string]bool{"metrics": true, "events": true, "logs": true, "spans": true, "projects": false}

	for _, tc := range []struct {
		name     string
		sql      string
		wantFlag bool
	}{
		{
			name:     "plain CHECK on partitioned parent is unsafe",
			sql:      `ALTER TABLE metrics ADD CONSTRAINT metrics_value_finite CHECK (value <> 'NaN'::double precision);`,
			wantFlag: true,
		},
		{
			name:     "NOT VALID CHECK on partitioned parent is safe",
			sql:      `ALTER TABLE metrics ADD CONSTRAINT metrics_value_finite CHECK (value <> 'NaN'::double precision) NOT VALID;`,
			wantFlag: false,
		},
		{
			name:     "VALIDATE CONSTRAINT is not an ADD and is safe",
			sql:      `ALTER TABLE metrics VALIDATE CONSTRAINT metrics_value_finite;`,
			wantFlag: false,
		},
		{
			name:     "plain FOREIGN KEY on partitioned parent is unsafe",
			sql:      `ALTER TABLE events ADD CONSTRAINT events_proj_fk FOREIGN KEY (project_id) REFERENCES projects (id);`,
			wantFlag: true,
		},
		{
			name:     "NOT VALID FOREIGN KEY on partitioned parent is safe",
			sql:      `ALTER TABLE events ADD CONSTRAINT events_proj_fk FOREIGN KEY (project_id) REFERENCES projects (id) NOT VALID;`,
			wantFlag: false,
		},
		{
			name:     "plain CHECK on a NON-partitioned table is out of scope",
			sql:      `ALTER TABLE projects ADD CONSTRAINT projects_dsn_id_key UNIQUE (dsn_id);`,
			wantFlag: false,
		},
		{
			name:     "vary the spelling: schema-qualified ONLY still caught",
			sql:      `ALTER TABLE ONLY public.logs ADD CONSTRAINT logs_chk CHECK (level <> '');`,
			wantFlag: true,
		},
	} {
		got := unsafeAddConstraints(normalizeSQL(tc.sql), partitioned)
		if (len(got) > 0) != tc.wantFlag {
			t.Errorf("%s: flagged=%v want=%v (reasons=%v)", tc.name, len(got) > 0, tc.wantFlag, got)
		}
	}
}
