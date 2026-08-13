package ciguard

import "testing"

// scopeProbe is one query shape that satisfies orgPredicate on the RAW body
// while proving nothing about which rows the caller may read. Each was
// confirmed to pass the unreduced predicate before this file was written, so
// the reduction is load-bearing rather than incidentally green.
type scopeProbe struct {
	name string
	sql  string
	why  string
}

var scopeProbes = []scopeProbe{
	{
		name: "join_on_left",
		sql:  `SELECT i.id, i.title FROM issues i LEFT JOIN orgs o ON i.org_id = $1 WHERE i.project_id = $2`,
		why:  "on a LEFT JOIN the ON condition does not restrict the left table; every org's issues are returned",
	},
	{
		name: "join_on_inner",
		sql:  `SELECT i.id FROM issues i INNER JOIN orgs o ON i.org_id = $1`,
		why:  "a join condition is a relation between two rows, not a statement about which rows may be read",
	},
	{
		name: "block_comment",
		sql:  `SELECT id FROM issues WHERE project_id = $1 /* already scoped by org_id = $2 upstream */`,
		why:  "reLineComment strips -- only, so a block comment let prose satisfy a security guard",
	},
	{
		name: "line_comment",
		sql:  "SELECT id FROM issues WHERE project_id = $1 -- scoped by org_id = $2\n",
		why:  "already stripped upstream by reLineComment; kept so the reduction subsumes that step rather than depending on it",
	},
	{
		name: "projection_bare_expr",
		sql:  `SELECT id, (org_id = $1) AS is_mine FROM issues WHERE project_id = $2`,
		why:  "returning whether a row is yours is not filtering to the rows that are",
	},
	{
		name: "returning_expr",
		sql:  `UPDATE issues SET title = $1 WHERE id = $2 RETURNING id, (org_id = $3) AS mine`,
		why:  "same thing on the write path: RETURNING reports on the row already chosen",
	},
}

// TestScopeProbesBypassTheRawPredicate is the NEGATIVE CONTROL. If this ever
// fails, the probe stopped being a bypass and the case below proves nothing.
func TestScopeProbesBypassTheRawPredicate(t *testing.T) {
	for _, p := range scopeProbes {
		if !orgPredicate.MatchString(p.sql) {
			t.Errorf("probe %q no longer satisfies the raw orgPredicate, so it is not evidence of anything. "+
				"Either the predicate changed or the probe was edited; fix the probe, do not delete it.", p.name)
		}
	}
}

// TestScopeScanBodyClosesEveryProbe is the guard's reason to exist.
func TestScopeScanBodyClosesEveryProbe(t *testing.T) {
	for _, p := range scopeProbes {
		if orgPredicate.MatchString(scopeScanBody(p.sql)) {
			t.Errorf("probe %q still satisfies orgPredicate after reduction: %s\nreduced: %s",
				p.name, p.why, scopeScanBody(p.sql))
		}
	}
}

// TestScopeScanBodyKeepsRealScoping is the false-positive control. A reduction
// that failed correctly scoped queries would be worse than no reduction, since
// the fix would be to widen the allow-unscoped list.
func TestScopeScanBodyKeepsRealScoping(t *testing.T) {
	scoped := []struct{ name, sql string }{
		{"plain_where", `SELECT id FROM issues WHERE org_id = $1 AND project_id = $2`},
		{"sqlc_arg", `SELECT id FROM issues WHERE org_id = sqlc.arg('org_id')`},
		{"named_arg", `SELECT id FROM issues WHERE org_id = @org_id`},
		{"where_after_join", `SELECT i.id FROM issues i JOIN orgs o ON i.org_id = o.id WHERE i.org_id = $1`},
		{"scoped_subselect_and_outer", `SELECT r.id, (SELECT count(*) FROM issues i WHERE i.org_id = $1) AS n FROM releases r WHERE r.org_id = $1`},
		{"in_list", `SELECT id FROM issues WHERE org_id IN (SELECT org_id FROM memberships WHERE user_id = $1)`},
		{"delete_scoped", `DELETE FROM issues WHERE org_id = $1 AND id = $2 RETURNING id`},
	}
	for _, s := range scoped {
		if !orgPredicate.MatchString(scopeScanBody(s.sql)) {
			t.Errorf("correctly scoped query %q was broken by the reduction:\nreduced: %s",
				s.name, scopeScanBody(s.sql))
		}
	}
}

// KNOWN RESIDUAL GAP (measured 2026-08-04, deliberately not closed here).
//
// A scoped scalar sub-select in the projection satisfies the guard even when
// the OUTER query is unscoped:
//
//	SELECT r.id, (SELECT count(*) FROM issues i WHERE i.org_id = $1) AS n
//	FROM releases r WHERE r.project_id = $2
//
// blankProjection deliberately preserves nested SELECT spans so that genuinely
// scoped aggregates do not false-fail, and the preserved sub-select carries a
// real bound parameter. brightcrm made the same trade knowingly.
//
// This is NOT closed by a body reduction. Closing it needs the predicate to be
// attributed to the DRIVING table rather than to the statement, which is a
// different change. Pinned here so the gap is a known quantity rather than a
// silent assumption, and so a future attributed-predicate pass has a failing
// case to start from.
func TestKnownResidual_ScopedSubselectSatisfiesUnscopedOuter(t *testing.T) {
	sql := `SELECT r.id, (SELECT count(*) FROM issues i WHERE i.org_id = $1) AS n FROM releases r WHERE r.project_id = $2`
	if !orgPredicate.MatchString(scopeScanBody(sql)) {
		t.Log("residual gap has been closed; delete this test and the note above it")
	}
}
