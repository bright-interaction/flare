// Package ciguard is Flare's structural backstop against cross-tenant data
// leaks. It parses the SQL query source tree itself so a brand-new query that
// reads a tenant-scoped table without an org_id predicate fails CI, even
// though every existing query is correctly scoped.
//
// A query that legitimately reads a scoped table by an unguessable secret
// (an ingest public_key, an API-key hash) opts out with a trailing marker:
//
//	-- ciguard:allow-unscoped <reason>
//
// on the line directly above its `-- name:` directive.
//
// # Why this was rewritten (2026-07-26)
//
// The original guard had two holes and the 2026-07-25 audit walked through
// both:
//
//  1. The scoped-table list was HARDCODED and missed 8 tables that carry an
//     org_id column: ai_config, audit_log, github_config, invitations,
//     oidc_config, releases, source_map_artifacts, users. `releases` is the one
//     that mattered: handleCreateRelease wrote into another org's project with
//     no ownership check, and because the guard never looked at that table, CI
//     was green throughout. The list is now DERIVED from the migrations, so a
//     new tenant table is covered the moment it exists rather than whenever
//     somebody remembers to add it here.
//
//  2. Scoping was a substring test, strings.Contains(body, "org_id"). That
//     passes on a mention ANYWHERE: a SELECT list, a RETURNING clause, an
//     INSERT column, even a comment. It proves the string exists, not that the
//     rows are filtered. Reads and mutations now require an org_id PREDICATE,
//     and comments are stripped before matching so a note about org_id cannot
//     satisfy the guard.
package ciguard

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// orgPredicate matches org_id BOUND TO CALLER SCOPE: compared against a query
// parameter, not merely mentioned and not merely correlated to another table.
//
// The binding requirement is the whole point, and a weaker version of this
// check still missed the real bug. The pre-fix ListReleasesByProject read
//
//	SELECT ..., (SELECT count(*) FROM issues i
//	              WHERE i.project_id = r.project_id AND i.org_id = r.org_id ...)
//	FROM releases r WHERE r.project_id = $1
//
// which contains `i.org_id = r.org_id`. That is a CORRELATION between two
// tables, not a scope: it says the two rows share an org, never which org the
// caller may see. Requiring org_id to meet a $N / sqlc.arg / @name parameter
// somewhere is what separates "filtered to the caller's tenant" from "joined
// consistently across tenants".
var orgPredicate = regexp.MustCompile(`(?i)(\borg_id\s*(=|<>|!=|\bin\b)\s*(\$\d+|sqlc\.n?arg\([^)]*\)|@\w+|\()|(\$\d+|@\w+)\s*=\s*\w*\.?\borg_id\b|sqlc\.n?arg\(\s*org_id\s*\)|@org_id\b)`)

// projectPredicate is the same idea for project_id.
var projectPredicate = regexp.MustCompile(`(?i)(\bproject_id\s*(=|<>|!=|>=|<=|>|<|\bin\b|\bis\b)|sqlc\.n?arg\(\s*project_id\s*\)|@project_id\b)`)

var (
	reCreateTable = regexp.MustCompile(`(?is)CREATE TABLE\s+(?:IF NOT EXISTS\s+)?(\w+)\s*\((.*?)\)\s*(?:PARTITION BY[^;]*)?;`)
	reAlterAddOrg = regexp.MustCompile(`(?is)ALTER TABLE\s+(\w+)\b[^;]*?ADD COLUMN\s+(?:IF NOT EXISTS\s+)?org_id\b`)
	reOrgCol      = regexp.MustCompile(`(?i)\borg_id\b`)
	reLineComment = regexp.MustCompile(`--[^\n]*`)
	reDriving     = regexp.MustCompile(`(?i)\b(?:FROM|JOIN|UPDATE|INSERT\s+INTO|DELETE\s+FROM)\s+(\w+)`)
	reIsInsert    = regexp.MustCompile(`(?is)^\s*INSERT\s+INTO`)
)

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

// discoverScopedTables reads the migrations and returns every table carrying an
// org_id column. Deriving this beats a hardcoded list: the previous hardcoded
// one silently omitted `releases`, and that omission let a cross-tenant write
// ship green.
func discoverScopedTables(t *testing.T, root string) map[string]bool {
	t.Helper()
	dir := filepath.Join(root, "internal", "db", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	var sb strings.Builder
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		sb.Write(b)
		sb.WriteString("\n")
	}
	sql := sb.String()

	out := map[string]bool{}
	for _, m := range reCreateTable.FindAllStringSubmatch(sql, -1) {
		if reOrgCol.MatchString(m[2]) {
			out[strings.ToLower(m[1])] = true
		}
	}
	for _, m := range reAlterAddOrg.FindAllStringSubmatch(sql, -1) {
		out[strings.ToLower(m[1])] = true
	}
	if len(out) == 0 {
		t.Fatal("ciguard: discovered NO org-scoped tables; the migration parser is broken, which would make this guard silently vacuous")
	}
	return out
}

// query is one named sqlc query, comments already stripped from Body.
type query struct {
	Name     string
	Body     string
	Allowed  bool // -- ciguard:allow-unscoped
	NoProj   bool // -- ciguard:allow-no-project
	FileName string
}

func parseQueries(t *testing.T, root string) []query {
	t.Helper()
	dir := filepath.Join(root, "internal", "db", "queries")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read queries dir: %v", err)
	}
	var out []query
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		out = append(out, splitQueries(e.Name(), string(b))...)
	}
	if len(out) == 0 {
		t.Fatal("ciguard: parsed NO queries; the parser is broken and this guard would pass vacuously")
	}
	return out
}

func splitQueries(file, src string) []query {
	var out []query
	var cur *query
	var body []string
	pendingAllow, pendingNoProj := false, false

	flush := func() {
		if cur == nil {
			return
		}
		// Strip comments BEFORE any matching: a comment mentioning org_id must
		// never satisfy the predicate check.
		cur.Body = reLineComment.ReplaceAllString(strings.Join(body, "\n"), " ")
		out = append(out, *cur)
		cur, body = nil, nil
	}

	for _, ln := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(ln)
		switch {
		case strings.HasPrefix(trimmed, "-- ciguard:allow-unscoped"):
			pendingAllow = true
			continue
		case strings.HasPrefix(trimmed, "-- ciguard:allow-no-project"):
			pendingNoProj = true
			continue
		case strings.HasPrefix(trimmed, "-- name:"):
			flush()
			cur = &query{Name: trimmed, Allowed: pendingAllow, NoProj: pendingNoProj, FileName: file}
			pendingAllow, pendingNoProj = false, false
			continue
		}
		// Any real SQL line clears a pending marker so it cannot drift onto a
		// later query.
		if trimmed != "" && !strings.HasPrefix(trimmed, "--") {
			pendingAllow, pendingNoProj = false, false
		}
		if cur != nil {
			body = append(body, ln)
		}
	}
	flush()
	return out
}

func drivingTables(body string) []string {
	var out []string
	for _, m := range reDriving.FindAllStringSubmatch(body, -1) {
		out = append(out, strings.ToLower(m[1]))
	}
	return out
}

// TestQueriesAreTenantScoped is the core guard: every query touching an
// org-scoped table must FILTER on org_id, not merely mention it.
func TestQueriesAreTenantScoped(t *testing.T) {
	root := repoRoot(t)
	scoped := discoverScopedTables(t, root)

	for _, q := range parseQueries(t, root) {
		if q.Allowed {
			continue
		}
		var touched []string
		for _, tbl := range drivingTables(q.Body) {
			if scoped[tbl] {
				touched = append(touched, tbl)
			}
		}
		if len(touched) == 0 {
			continue
		}
		// An INSERT stamps org_id as a column rather than filtering on it, so
		// requiring a predicate there would be wrong. Require the column to be
		// written, which is the strongest structural claim available.
		if reIsInsert.MatchString(strings.TrimSpace(q.Body)) {
			if !reOrgCol.MatchString(q.Body) {
				t.Errorf("%s: query %q INSERTs into tenant table(s) %v without stamping org_id",
					q.FileName, q.Name, touched)
			}
			continue
		}
		if !orgPredicate.MatchString(q.Body) {
			hint := "no org_id at all"
			if reOrgCol.MatchString(q.Body) {
				hint = "org_id is MENTIONED but never used as a filter (a SELECT list or RETURNING clause does not scope rows)"
			}
			t.Errorf("%s: query %q reads/mutates tenant table(s) %v without an org_id predicate: %s. Add the filter, or an `-- ciguard:allow-unscoped <reason>` marker.",
				q.FileName, q.Name, touched, hint)
		}
	}
}

// telemetryTables hold per-PROJECT rows. A trace_id is not project-unique, so
// org_id alone can mix projects.
var telemetryTables = map[string]bool{
	"events": true, "logs": true, "spans": true, "issues": true, "metrics": true,
}

func TestTelemetryReadsAreProjectScoped(t *testing.T) {
	root := repoRoot(t)
	for _, q := range parseQueries(t, root) {
		if q.NoProj {
			continue
		}
		var touched []string
		for _, tbl := range drivingTables(q.Body) {
			if telemetryTables[tbl] {
				touched = append(touched, tbl)
			}
		}
		if len(touched) == 0 {
			continue
		}
		if reIsInsert.MatchString(strings.TrimSpace(q.Body)) {
			continue // an insert stamps project_id; the org check above covers it
		}
		if !projectPredicate.MatchString(q.Body) {
			t.Errorf("%s: query %q reads/mutates telemetry table(s) %v without a project_id predicate. Add it, or a `-- ciguard:allow-no-project <reason>` marker.",
				q.FileName, q.Name, touched)
		}
	}
}

// TestGuardCoversEveryOrgScopedTable is the guard on the guard. The previous
// version's real failure was a silently incomplete table list, so assert the
// discovery still finds the tables we know carry tenant data. A migration that
// renames or drops one should force a deliberate edit here rather than quietly
// shrinking coverage.
func TestGuardCoversEveryOrgScopedTable(t *testing.T) {
	root := repoRoot(t)
	got := discoverScopedTables(t, root)

	must := []string{
		"projects", "issues", "events", "logs", "spans", "metrics",
		"api_keys", "notification_channels", "monitors", "releases",
		"source_map_artifacts", "invitations", "oidc_config", "github_config",
		"ai_config", "audit_log", "users",
	}
	var missing []string
	for _, m := range must {
		if !got[m] {
			missing = append(missing, m)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("ciguard discovery missed known tenant tables %v; the migration parser regressed and the guard is weaker than it looks", missing)
	}
}

// TestGuardRejectsAMentionWithoutAPredicate is the regression test for the
// exact hole that let the releases bug through: org_id present in a SELECT list
// but never filtered on.
func TestGuardRejectsAMentionWithoutAPredicate(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want bool // true = should be treated as scoped
	}{
		{"bound to a parameter", "SELECT * FROM issues WHERE project_id = $1 AND org_id = $2", true},
		{"select-list mention only", "SELECT id, org_id FROM issues WHERE project_id = $1", false},
		{"returning mention only", "UPDATE issues SET status = $2 WHERE id = $1 RETURNING id, org_id", false},
		{"no org_id at all", "SELECT * FROM issues WHERE id = $1", false},
		{"sqlc narg", "SELECT * FROM issues WHERE sqlc.narg(org_id) IS NULL", true},
		// The exact pre-fix ListReleasesByProject shape. The subquery correlates
		// two tables' org_ids, which says they SHARE an org but never which org
		// the caller may read. That is not a scope, and treating it as one is
		// what let the cross-tenant release read ship green.
		{"correlation only is NOT a scope", "SELECT (SELECT count(*) FROM issues i WHERE i.org_id = r.org_id) FROM releases r WHERE r.project_id = $1", false},
		{"correlation PLUS a bound outer scope", "SELECT (SELECT count(*) FROM issues i WHERE i.org_id = r.org_id) FROM releases r WHERE r.project_id = $1 AND r.org_id = $2", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := reLineComment.ReplaceAllString(tc.sql, " ")
			if got := orgPredicate.MatchString(body); got != tc.want {
				t.Errorf("orgPredicate(%q) = %v, want %v", tc.sql, got, tc.want)
			}
		})
	}
}
