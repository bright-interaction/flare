// Package dbtest holds the tree's only tests that run against a REAL Postgres.
//
// Every audit of this repo has ended the same way: "no live Postgres was
// reachable, so every SQL claim is from reading". The 2026-08-11 audit's
// coverage boundary says it outright, and the repo's own gotcha says a green
// `go test` is not evidence a query behaves as written, because a prod outage
// already shipped from a sqlc query that `go vet` accepted.
//
// So the claims that can only be settled by executing SQL are settled here.
// Skipped without TEST_DATABASE_URL, so the ordinary suite is unchanged:
//
//	docker run -d --name pg -p 5432:5432 -e POSTGRES_PASSWORD=x postgres:16-alpine
//	TEST_DATABASE_URL='postgres://postgres:x@localhost:5432/postgres' go test ./internal/db/dbtest/
package dbtest

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bright-interaction/flare/internal/db"
	"github.com/bright-interaction/flare/internal/db/generated"
	"github.com/bright-interaction/flare/internal/id"
)

func newPool(t *testing.T) (*pgxpool.Pool, *generated.Queries) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run the SQL behaviour tests against a real Postgres")
	}
	ctx := context.Background()
	// The migrations are the thing under test as much as the queries are:
	// running them here is what proves 030 applies to a database that has had
	// every earlier migration run against it, in order.
	if err := db.RunMigrations(ctx, url); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, generated.New(pool)
}

// seedProject creates an org + project and returns their ids.
func seedProject(t *testing.T, ctx context.Context, q *generated.Queries) (org, project string) {
	t.Helper()
	o, err := q.CreateOrg(ctx, generated.CreateOrgParams{ID: id.New(), Name: "t", Slug: id.New()})
	if err != nil {
		t.Fatalf("create org: %v", err)
	}
	p, err := q.CreateProject(ctx, generated.CreateProjectParams{
		ID: id.New(), OrgID: o.ID, Name: "t", Slug: id.New(), Platform: "go",
		PublicKey: id.New(), DsnID: id.New()[:12],
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return o.ID, p.ID
}

func seedIssue(t *testing.T, ctx context.Context, q *generated.Queries, org, project, title, level string) string {
	t.Helper()
	row, err := q.UpsertIssue(ctx, generated.UpsertIssueParams{
		ID: id.New(), ProjectID: project, OrgID: org, Fingerprint: id.New(),
		Title: title, Culprit: "c", Level: level, Platform: "go",
	})
	if err != nil {
		t.Fatalf("upsert issue %q: %v", title, err)
	}
	return row.ID
}

// M1's SQL half, which the audit said to confirm against a real database
// before signing off. Confirmed, and the finding was WRONG.
//
// The claim was that without ESCAPE '\' the backslashes escapeLike writes are
// inert, so a "%" in a search term still wildcards. On PostgreSQL that is
// false: backslash is ALREADY the default LIKE escape character. This test
// runs the same term through the generated query (which has the clause) and
// through raw SQL (which does not) and asserts they agree.
//
// What that leaves is the real M1: the Go side. The logs search term got no
// escapeLike, no SanitizeText and no length cap, which is where all three of
// the finding's named harms actually came from, and those are fixed.
func TestLikeEscapeClauseMatchesThePostgresDefault(t *testing.T) {
	ctx := context.Background()
	pool, q := newPool(t)
	org, project := seedProject(t, ctx, q)

	// One issue whose title contains a LITERAL percent, and one that does not.
	seedIssue(t, ctx, q, org, project, "checkout failed at 50% capacity", "error")
	seedIssue(t, ctx, q, org, project, "unrelated timeout", "error")

	// escapeLike("50%") produces this.
	escaped := `50\%`

	// Through the generated query, which carries ESCAPE '\'.
	withClause, err := q.ListIssues(ctx, generated.ListIssuesParams{
		ProjectID: project, OrgID: org, Limit: 50, Offset: 0,
		Q: pgtype.Text{String: escaped, Valid: true},
	})
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(withClause) != 1 {
		t.Fatalf("with ESCAPE, %q matched %d issues, want exactly the literal one", escaped, len(withClause))
	}

	// The same term with NO clause. If the audit's claim held, this would
	// wildcard and match both rows.
	var withoutClause int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM issues WHERE project_id = $1 AND org_id = $2 AND title ILIKE '%' || $3 || '%'`,
		project, org, escaped).Scan(&withoutClause); err != nil {
		t.Fatalf("no-clause control: %v", err)
	}
	if withoutClause != len(withClause) {
		t.Fatalf("with ESCAPE matched %d rows and without it %d. If this ever fails, the clause "+
			"HAS become load-bearing (a changed server default or a different engine) and the "+
			"comments in events.sql, logs.sql and the ciguard rule need reverting to the "+
			"original justification.", len(withClause), withoutClause)
	}
	t.Logf("ESCAPE '\\' is a no-op on this server: %d rows either way, "+
		"standard_conforming_strings is on and backslash is already the default", withoutClause)

	// The escaping ITSELF is load-bearing, clause or no clause: an UNescaped
	// bare %% is a wildcard and matches everything, which is what escapeLike
	// exists to prevent and what the logs pillar had no protection against.
	var unescaped int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM issues WHERE project_id = $1 AND org_id = $2 AND title ILIKE '%' || $3 || '%'`,
		project, org, "%").Scan(&unescaped); err != nil {
		t.Fatalf("wildcard control: %v", err)
	}
	if unescaped != 2 {
		t.Fatalf("an unescaped %% matched %d rows, want both: escapeLike is what actually matters here", unescaped)
	}
}

// The logs pillar's copy of the same clause, which is the one that was missing.
func TestLogSearchEscapeIsHonoured(t *testing.T) {
	ctx := context.Background()
	_, q := newPool(t)
	org, project := seedProject(t, ctx, q)

	now := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	for _, body := range []string{"disk at 90% full", "nothing to see"} {
		if _, err := q.InsertLogs(ctx, []generated.InsertLogsParams{{
			ID: id.New(), ProjectID: project, OrgID: org,
			Severity: "info", Body: body, Attributes: []byte(`{}`), ObservedAt: now,
		}}); err != nil {
			t.Fatalf("insert log: %v", err)
		}
	}

	term := `90\%`
	since := pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true}
	rows, err := q.SearchLogs(ctx, generated.SearchLogsParams{
		ProjectID: project, OrgID: org, Limit: 50, Q: pgtype.Text{String: term, Valid: true}, Since: since,
	})
	if err != nil {
		t.Fatalf("SearchLogs: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("log search for %q matched %d rows, want 1. The ESCAPE clause is not doing its job.", term, len(rows))
	}
}

// The portability export must return EVERY issue, including info/debug, and
// must page stably under live ingest. ListIssues carries
// `level NOT IN ('info','debug')` for the dashboard and handleExport reused it.
func TestExportReturnsInfoAndDebugIssues(t *testing.T) {
	ctx := context.Background()
	_, q := newPool(t)
	org, project := seedProject(t, ctx, q)

	for _, lvl := range []string{"error", "warning", "info", "debug"} {
		seedIssue(t, ctx, q, org, project, "issue-"+lvl, lvl)
	}

	listed, err := q.ListIssues(ctx, generated.ListIssuesParams{
		ProjectID: project, OrgID: org, Limit: 50, Offset: 0,
	})
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("dashboard list returned %d issues, want 2 (error + warning)", len(listed))
	}

	exported, err := q.ListAllIssuesForExport(ctx, generated.ListAllIssuesForExportParams{
		ProjectID: project, OrgID: org, Limit: 50,
	})
	if err != nil {
		t.Fatalf("ListAllIssuesForExport: %v", err)
	}
	if len(exported) != 4 {
		t.Fatalf("export returned %d issues, want all 4. A portability bundle that drops rows is the finding.", len(exported))
	}

	// Keyset paging: page 2 continues from page 1's last row with no overlap
	// and no gap, which is what OFFSET over a mutating sort column could not do.
	page1, err := q.ListAllIssuesForExport(ctx, generated.ListAllIssuesForExportParams{
		ProjectID: project, OrgID: org, Limit: 2,
	})
	if err != nil || len(page1) != 2 {
		t.Fatalf("page 1: %v (%d rows)", err, len(page1))
	}
	last := page1[len(page1)-1]
	page2, err := q.ListAllIssuesForExport(ctx, generated.ListAllIssuesForExportParams{
		ProjectID: project, OrgID: org, Limit: 2,
		AfterLastSeen: last.LastSeen, AfterID: pgtype.Text{String: last.ID, Valid: true},
	})
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	seen := map[string]bool{}
	for _, r := range append(append([]*generated.Issue{}, page1...), page2...) {
		if seen[r.ID] {
			t.Fatalf("keyset paging returned issue %s twice", r.ID)
		}
		seen[r.ID] = true
	}
	if len(seen) != 4 {
		t.Fatalf("two pages of 2 covered %d distinct issues, want 4", len(seen))
	}
}

// Migration 030 folds issues.level to lowercase. The Go boundary canonicalises
// new writes; this covers the rows that already existed.
func TestMigration030FoldsExistingLevels(t *testing.T) {
	ctx := context.Background()
	pool, q := newPool(t)
	org, project := seedProject(t, ctx, q)
	issueID := seedIssue(t, ctx, q, org, project, "legacy", "error")

	// Write the pre-fix shape directly, the way an OTLP-conventional service
	// used to land it.
	if _, err := pool.Exec(ctx, `UPDATE issues SET level = 'INFO' WHERE id = $1`, issueID); err != nil {
		t.Fatalf("plant uppercase level: %v", err)
	}
	// The defect: the dashboard's predicate is case-sensitive, so an
	// informational row counts as an incident.
	listed, err := q.ListIssues(ctx, generated.ListIssuesParams{ProjectID: project, OrgID: org, Limit: 50})
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("setup: expected the uppercase INFO row to slip through the filter, got %d rows", len(listed))
	}

	// 030's statement, applied the way the migration applies it.
	if _, err := pool.Exec(ctx,
		`UPDATE issues SET level = lower(btrim(level)) WHERE level <> lower(btrim(level))`); err != nil {
		t.Fatalf("migration 030 statement: %v", err)
	}
	listed, err = q.ListIssues(ctx, generated.ListIssuesParams{ProjectID: project, OrgID: org, Limit: 50})
	if err != nil {
		t.Fatalf("ListIssues after 030: %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("after 030 the folded INFO row still counts as an incident (%d rows)", len(listed))
	}
}

// The new LIMIT parameters have to be real parameters that bound real rows,
// not just fields sqlc generated.
func TestNewReadCapsBound(t *testing.T) {
	ctx := context.Background()
	_, q := newPool(t)
	org, project := seedProject(t, ctx, q)

	now := time.Now()
	traceID := id.New()
	spans := make([]generated.InsertSpansParams, 0, 10)
	metrics := make([]generated.InsertMetricsParams, 0, 10)
	for i := 0; i < 10; i++ {
		spans = append(spans, generated.InsertSpansParams{
			TraceID: traceID, SpanID: id.New(), ProjectID: project, OrgID: org,
			Name: "op", Kind: "server", Status: "ok",
			StartTime:  pgtype.Timestamptz{Time: now, Valid: true},
			EndTime:    pgtype.Timestamptz{Time: now.Add(time.Millisecond), Valid: true},
			Attributes: []byte(`{}`),
		})
		metrics = append(metrics, generated.InsertMetricsParams{
			ID: id.New(), ProjectID: project, OrgID: org,
			Name: "m" + id.New()[:8], Kind: "gauge", Value: 1,
			Labels: []byte(`{}`), ObservedAt: pgtype.Timestamptz{Time: now, Valid: true},
		})
	}
	if _, err := q.InsertSpans(ctx, spans); err != nil {
		t.Fatalf("insert spans: %v", err)
	}
	if _, err := q.InsertMetrics(ctx, metrics); err != nil {
		t.Fatalf("insert metrics: %v", err)
	}

	got, err := q.GetTraceSpans(ctx, generated.GetTraceSpansParams{
		TraceID: traceID, ProjectID: project, OrgID: org, Limit: 3,
	})
	if err != nil {
		t.Fatalf("GetTraceSpans: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("GetTraceSpans returned %d spans against LIMIT 3; the cap is not applied", len(got))
	}

	names, err := q.ListMetricNames(ctx, generated.ListMetricNamesParams{
		ProjectID: project, OrgID: org, Limit: 4,
	})
	if err != nil {
		t.Fatalf("ListMetricNames: %v", err)
	}
	if len(names) != 4 {
		t.Fatalf("ListMetricNames returned %d names against LIMIT 4; the cap is not applied", len(names))
	}

	// ListTraces gained a time predicate. A window that excludes the spans
	// must return nothing, or the predicate is decorative.
	inWindow, err := q.ListTraces(ctx, generated.ListTracesParams{
		ProjectID: project, OrgID: org, Limit: 10,
		Since: pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatalf("ListTraces: %v", err)
	}
	if len(inWindow) != 1 {
		t.Fatalf("ListTraces in-window returned %d traces, want 1", len(inWindow))
	}
	outOfWindow, err := q.ListTraces(ctx, generated.ListTracesParams{
		ProjectID: project, OrgID: org, Limit: 10,
		Since: pgtype.Timestamptz{Time: now.Add(time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatalf("ListTraces(future): %v", err)
	}
	if len(outOfWindow) != 0 {
		t.Fatalf("ListTraces ignored its time predicate: %d traces from a future window", len(outOfWindow))
	}
}
