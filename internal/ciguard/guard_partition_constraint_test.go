package ciguard

// Structural backstop for the partitioned-parent DDL trap family (findings D3 +
// B3, 2026-07-29 audit).
//
// A partitioned table has a parent plus one child per partition. Any DDL that
// takes a wide lock on the parent cascades to EVERY child at once, and these
// migrations run in-process at server boot, so the whole ingest path stalls for
// the operation's duration. Two shapes of this bite:
//
//   1. ADD CONSTRAINT (D3). Migration 025 originally added a CHECK to the
//      PARTITIONED metrics parent with a plain ADD CONSTRAINT. That takes
//      AccessExclusive on the parent AND every child and scans every row before
//      it commits, so it is a metric-ingest outage for the scan's duration and a
//      single pre-existing bad row rolls back the entire deploy. The safe form is
//      ADD CONSTRAINT ... NOT VALID (metadata-only, no scan, still enforced on
//      new writes) followed by a separate VALIDATE CONSTRAINT (migration 029)
//      that scans under ShareUpdateExclusive without blocking ingest. The same
//      trap applies to an anonymous ADD CHECK / ADD FOREIGN KEY (no CONSTRAINT
//      name), which is the same full-scan operation spelled differently.
//
//   2. CREATE INDEX (B3). Migration 014 issues a plain CREATE INDEX on the
//      partitioned events parent AFTER events was created (in 002). On a
//      populated parent the plain form takes ShareLock on the parent and every
//      child for the whole build, blocking all ingest; the CONCURRENTLY form is
//      rejected outright by PostgreSQL on a partitioned table. Either spelling is
//      unsafe post-creation. An index built in the SAME migration that creates
//      the (still empty) parent, as in 002 and 021, is baseline schema and safe.
//
// This guard fails CI if any migration issues either unsafe shape against a
// partitioned parent it did not itself create, so the next person who reaches for
// one of these is stopped at the trap rather than reintroducing the outage.
//
// The one known pre-existing violation (014's CREATE INDEX) is grandfathered in
// grandfatheredPartitionDDL below: it already shipped, is already applied on every
// deployed database, and self-heals on fresh installs (the events table is empty
// when 014 runs), so editing 014 now would be a no-op on deployed DBs and is
// forbidden anyway (CLAUDE.md: never retro-edit a shipped migration). The guard
// stays LOUD for any NEW violation.

import (
	"os"
	"path/filepath"
	"regexp"
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
	// A CREATE INDEX ... ON <table> statement, capturing the target table through
	// the same spellings (CONCURRENTLY, IF NOT EXISTS, UNIQUE, schema qualifier,
	// ONLY, quotes normalized away first). The index name is a single token; after
	// normalizeSQL a quoted name has become a space-delimited token, so \S+ still
	// matches it.
	reCreateIndexOn = regexp.MustCompile(`(?is)\bCREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+NOT\s+EXISTS\s+)?\S+\s+ON\s+(?:ONLY\s+)?(?:\w+\.)?(\w+)`)
	// An ADD of a scan-bearing constraint in ALL FOUR spellings: named or
	// anonymous CHECK, named or anonymous FOREIGN KEY. CHECK and FOREIGN KEY are
	// the two that scan existing rows and support NOT VALID; UNIQUE / PRIMARY KEY
	// build an index instead and have no NOT VALID escape hatch, so they are
	// deliberately not matched (flagging them would be unactionable noise).
	reAddScanConstraint = regexp.MustCompile(`(?is)\bADD\s+(?:CONSTRAINT\s+\S+\s+)?(?:CHECK\b|FOREIGN\s+KEY\b)`)
	reMigVer            = regexp.MustCompile(`^(\d+)_`)
)

// grandfatheredPartitionDDL lists partitioned-parent DDL violations that shipped
// before this guard existed, keyed by "<migration file>\t<reason>". Each is a
// one-time upgrade stall that is already applied on every deployed database and
// self-heals on fresh installs, so re-editing the migration is a no-op on
// deployed DBs and forbidden by policy (never retro-edit a shipped migration).
//
// TODO(flare): 014_watchdog.sql's idx_events_project_received is a plain
// CREATE INDEX on the partitioned events parent (created in 002). It is a
// one-time upgrade stall, self-healing on fresh installs, and already shipped. A
// forward-only safe re-creation via the per-partition CREATE CONCURRENTLY + ATTACH
// dance is possible but not required. Do NOT add new entries here without the same
// justification: a NEW violation must be fixed, not grandfathered.
var grandfatheredPartitionDDL = map[string]bool{
	"014_watchdog.sql\t" + indexReason("events"): true,
}

func indexReason(table string) string {
	return "CREATE INDEX on partitioned parent " + table + " post-creation (locks parent + every child, or is rejected as CONCURRENTLY)"
}

func constraintReason(table string) string {
	return "ALTER TABLE " + table + " adds a scan-bearing constraint (CHECK/FOREIGN KEY) without NOT VALID"
}

// unsafeAddConstraints returns a reason for every ALTER TABLE statement in sql
// that adds a scan-bearing constraint (CHECK or FOREIGN KEY, named or anonymous)
// to a table in partitioned WITHOUT NOT VALID.
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
		if !reAddScanConstraint.MatchString(stmt) {
			continue
		}
		// NOT VALID defers the scan: metadata-only, enforced on new writes, safe.
		if strings.Contains(strings.ToUpper(stmt), "NOT VALID") {
			continue
		}
		out = append(out, constraintReason(table))
	}
	return out
}

// unsafeCreateIndexes returns a reason for every CREATE INDEX in sql that targets
// a partitioned parent NOT created in the same migration (createdHere). An index
// built in the migration that creates the still-empty parent is baseline schema
// and safe; any later one is a post-creation full build on a populated,
// partitioned table, or an illegal CONCURRENTLY, and blocks ingest either way.
func unsafeCreateIndexes(sql string, partitioned, createdHere map[string]bool) []string {
	var out []string
	for _, m := range reCreateIndexOn.FindAllStringSubmatch(sql, -1) {
		table := strings.ToLower(m[1])
		if !partitioned[table] || createdHere[table] {
			continue
		}
		out = append(out, indexReason(table))
	}
	return out
}

// stripLineComments removes -- comments so the scan sees only executable SQL.
// reLineComment is defined in guard_tenant_scoping_test.go.
func stripLineComments(sql string) string {
	return reLineComment.ReplaceAllString(sql, "")
}

// partitionViolation is one flagged unsafe DDL, before the grandfather allow-list
// is applied.
type partitionViolation struct {
	File   string
	Reason string
}

// scanPartitionDDL walks the real migrations and returns every unsafe
// partitioned-parent DDL statement it finds, WITHOUT subtracting the grandfather
// allow-list. Both the enforcing test and its meta-test build on this so they
// share one parser.
func scanPartitionDDL(t *testing.T) []partitionViolation {
	t.Helper()
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "db", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}

	// Discover partitioned parents AND the migration file that creates each one.
	// The origin file is what separates a safe in-creation index from an unsafe
	// post-creation one, and it is more precise than a version cutoff: events is
	// created in 002 but indexed in 014, so a "skip everything <= 21" rule would
	// miss 014 entirely (which is exactly B3).
	partitioned := map[string]bool{}
	origin := map[string]string{}
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
			tbl := strings.ToLower(m[1])
			partitioned[tbl] = true
			if _, seen := origin[tbl]; !seen {
				origin[tbl] = e.Name()
			}
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

	var out []partitionViolation
	for _, e := range files {
		if reMigVer.FindStringSubmatch(e.Name()) == nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		sql := normalizeSQL(stripLineComments(string(b)))

		createdHere := map[string]bool{}
		for tbl, f := range origin {
			if f == e.Name() {
				createdHere[tbl] = true
			}
		}

		for _, r := range unsafeCreateIndexes(sql, partitioned, createdHere) {
			out = append(out, partitionViolation{File: e.Name(), Reason: r})
		}
		for _, r := range unsafeAddConstraints(sql, partitioned) {
			out = append(out, partitionViolation{File: e.Name(), Reason: r})
		}
	}
	return out
}

// TestNoUnsafePartitionedParentDDL is the enforcing walk: every unsafe
// partitioned-parent DDL statement in the real migrations must be either absent
// or explicitly grandfathered. A NEW one fails CI.
func TestNoUnsafePartitionedParentDDL(t *testing.T) {
	for _, v := range scanPartitionDDL(t) {
		key := v.File + "\t" + v.Reason
		if grandfatheredPartitionDDL[key] {
			continue
		}
		t.Errorf("%s: %s. For CREATE INDEX use the per-partition CREATE CONCURRENTLY + ATTACH dance as deliberate maintenance; for a constraint use ADD CONSTRAINT ... NOT VALID plus a separate VALIDATE CONSTRAINT migration (see 025 + 029). Otherwise it locks the parent and every child and stalls ingest at boot. If this is a known pre-existing violation, add it to grandfatheredPartitionDDL with justification.", v.File, v.Reason)
	}
}

// TestGrandfatherListIsNotStale is the guard on the guard. Every grandfathered
// entry must correspond to a violation that STILL exists in the tree. If a
// grandfathered migration is later fixed or removed, its entry here would silently
// start suppressing nothing (or, worse, mask a future real violation that happened
// to reuse the same file+reason), so a stale entry is a failure. This is also what
// proves the CREATE INDEX detector actually fires against the real 014: if the
// detector regressed to never flagging, 014's entry would be reported stale here.
func TestGrandfatherListIsNotStale(t *testing.T) {
	live := map[string]bool{}
	for _, v := range scanPartitionDDL(t) {
		live[v.File+"\t"+v.Reason] = true
	}
	for key := range grandfatheredPartitionDDL {
		if !live[key] {
			t.Errorf("grandfatheredPartitionDDL entry %q no longer matches any violation in the tree; remove it (the migration was fixed) or fix the key. A stale allow-list entry weakens the guard.", key)
		}
	}
	// 014's CREATE INDEX on events is the specific B3 violation this pass added
	// coverage for. Pin it so a detector regression that stops flagging it is
	// caught here rather than passing silently.
	want := "014_watchdog.sql\t" + indexReason("events")
	if !live[want] {
		t.Errorf("expected the detector to flag 014's plain CREATE INDEX on the partitioned events parent (B3); it did not. The CREATE INDEX detector regressed and the guard is weaker than it looks.")
	}
}

// TestUnsafeAddConstraintDetection is the negative control for the constraint
// shape: it plants a real violation in each spelling and asserts the detector
// refuses it, then confirms the safe shapes pass. Without this, the walk passing
// tells us nothing, because a detector that never fires also passes.
func TestUnsafeAddConstraintDetection(t *testing.T) {
	partitioned := map[string]bool{"metrics": true, "events": true, "logs": true, "spans": true, "projects": false}

	for _, tc := range []struct {
		name     string
		sql      string
		wantFlag bool
	}{
		{
			name:     "plain named CHECK on partitioned parent is unsafe",
			sql:      `ALTER TABLE metrics ADD CONSTRAINT metrics_value_finite CHECK (value <> 'NaN'::double precision);`,
			wantFlag: true,
		},
		{
			name:     "anonymous ADD CHECK on partitioned parent is unsafe",
			sql:      `ALTER TABLE metrics ADD CHECK (value <> 'NaN'::double precision);`,
			wantFlag: true,
		},
		{
			name:     "NOT VALID CHECK on partitioned parent is safe",
			sql:      `ALTER TABLE metrics ADD CONSTRAINT metrics_value_finite CHECK (value <> 'NaN'::double precision) NOT VALID;`,
			wantFlag: false,
		},
		{
			name:     "anonymous NOT VALID CHECK on partitioned parent is safe",
			sql:      `ALTER TABLE metrics ADD CHECK (value <> 'NaN'::double precision) NOT VALID;`,
			wantFlag: false,
		},
		{
			name:     "VALIDATE CONSTRAINT is not an ADD and is safe",
			sql:      `ALTER TABLE metrics VALIDATE CONSTRAINT metrics_value_finite;`,
			wantFlag: false,
		},
		{
			name:     "plain named FOREIGN KEY on partitioned parent is unsafe",
			sql:      `ALTER TABLE events ADD CONSTRAINT events_proj_fk FOREIGN KEY (project_id) REFERENCES projects (id);`,
			wantFlag: true,
		},
		{
			name:     "anonymous ADD FOREIGN KEY on partitioned parent is unsafe",
			sql:      `ALTER TABLE events ADD FOREIGN KEY (project_id) REFERENCES projects (id);`,
			wantFlag: true,
		},
		{
			name:     "NOT VALID FOREIGN KEY on partitioned parent is safe",
			sql:      `ALTER TABLE events ADD CONSTRAINT events_proj_fk FOREIGN KEY (project_id) REFERENCES projects (id) NOT VALID;`,
			wantFlag: false,
		},
		{
			name:     "UNIQUE on partitioned parent is out of scope (no NOT VALID exists for it)",
			sql:      `ALTER TABLE metrics ADD CONSTRAINT metrics_u UNIQUE (id, observed_at);`,
			wantFlag: false,
		},
		{
			name:     "plain CHECK on a NON-partitioned table is out of scope",
			sql:      `ALTER TABLE projects ADD CONSTRAINT projects_chk CHECK (name <> '');`,
			wantFlag: false,
		},
		{
			name:     "vary the spelling: schema-qualified ONLY still caught",
			sql:      `ALTER TABLE ONLY public.logs ADD CONSTRAINT logs_chk CHECK (level <> '');`,
			wantFlag: true,
		},
		{
			name:     "vary the spelling: anonymous CHECK on schema-qualified ONLY still caught",
			sql:      `ALTER TABLE ONLY public.logs ADD CHECK (level <> '');`,
			wantFlag: true,
		},
	} {
		got := unsafeAddConstraints(normalizeSQL(tc.sql), partitioned)
		if (len(got) > 0) != tc.wantFlag {
			t.Errorf("%s: flagged=%v want=%v (reasons=%v)", tc.name, len(got) > 0, tc.wantFlag, got)
		}
	}
}

// TestUnsafeCreateIndexDetection is the negative control for the CREATE INDEX
// shape: it plants a real violation and varies every axis the guard depends on
// (partitioned vs plain, created-here vs post-creation, CONCURRENTLY, spelling of
// the table name).
func TestUnsafeCreateIndexDetection(t *testing.T) {
	partitioned := map[string]bool{"metrics": true, "events": true, "logs": true, "spans": true, "issues": false}

	for _, tc := range []struct {
		name        string
		sql         string
		createdHere map[string]bool
		wantFlag    bool
	}{
		{
			name:     "plain CREATE INDEX on partitioned parent post-creation is unsafe",
			sql:      `CREATE INDEX IF NOT EXISTS idx_events_project_received ON events (project_id, received_at);`,
			wantFlag: true,
		},
		{
			name:        "CREATE INDEX on partitioned parent in its OWN creating migration is safe",
			sql:         `CREATE INDEX idx_events_received_brin ON events USING BRIN (received_at);`,
			createdHere: map[string]bool{"events": true},
			wantFlag:    false,
		},
		{
			name:     "CONCURRENTLY on a partitioned parent post-creation is still unsafe (illegal on partitioned tables)",
			sql:      `CREATE INDEX CONCURRENTLY idx_events_x ON events (project_id);`,
			wantFlag: true,
		},
		{
			name:     "CREATE INDEX on a NON-partitioned table is out of scope",
			sql:      `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_issues_status ON issues (project_id, status);`,
			wantFlag: false,
		},
		{
			name:     "UNIQUE INDEX on partitioned parent post-creation is unsafe",
			sql:      `CREATE UNIQUE INDEX idx_metrics_u ON metrics (id, observed_at);`,
			wantFlag: true,
		},
		{
			name:     "vary the spelling: schema-qualified ONLY still caught",
			sql:      `CREATE INDEX idx_logs_trace ON ONLY public.logs (trace_id);`,
			wantFlag: true,
		},
		{
			name:     "vary the spelling: quoted table name still caught",
			sql:      `CREATE INDEX idx_spans_x ON "spans" (trace_id);`,
			wantFlag: true,
		},
	} {
		got := unsafeCreateIndexes(normalizeSQL(tc.sql), partitioned, tc.createdHere)
		if (len(got) > 0) != tc.wantFlag {
			t.Errorf("%s: flagged=%v want=%v (reasons=%v)", tc.name, len(got) > 0, tc.wantFlag, got)
		}
	}
}
