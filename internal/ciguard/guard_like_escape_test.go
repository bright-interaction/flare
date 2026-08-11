package ciguard

// Structural backstop for the "search term guards applied to one pillar and not
// its twin" family (audit finding M1, 2026-08-11).
//
// escapeLike writes a backslash in front of every LIKE metacharacter in a user
// search term. That backslash only MEANS anything if the query declares
// ESCAPE '\'. Without the clause Postgres treats the backslash as an ordinary
// character, so the metacharacter still matches as a wildcard and the escaping
// silently does nothing.
//
// That is exactly what shipped: the issues pillar got escapeLike AND the ESCAPE
// clause; the logs pillar, wired later, got neither, and the commit that wired
// it says in its own message "six surfaces in one commit, per the repo's
// incomplete-migration rule". An operator searching logs for "50%" or "user_id"
// got silently wrong rows, and nothing in the build noticed.
//
// So the pairing is enforced here rather than remembered: any ILIKE/LIKE
// against an interpolated search parameter must carry ESCAPE '\' in the same
// statement.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// reLikePattern finds a LIKE/ILIKE whose right-hand side interpolates a
// parameter, which is the only shape where a caller-supplied metacharacter can
// reach the matcher. A literal pattern with no parameter is the author's own
// wildcard (`status ILIKE '%error%'`) and is left alone.
//
// Matched per LINE, not per statement: a predicate wider than its own line
// would be missed, but scanning a whole statement made every query with a
// literal ILIKE anywhere in it and a $1 anywhere after it look like a
// violation. The found-nothing assertion below is what keeps the narrower form
// honest.
var reLikePattern = regexp.MustCompile(`(?i)\b(?:I?LIKE)\s+[^\n]*?(?:sqlc\.narg|sqlc\.arg|\$\d+)`)

func TestLikeAgainstAParameterDeclaresItsEscape(t *testing.T) {
	dir := "../db/queries"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read queries dir: %v", err)
	}
	found := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, line := range strings.Split(stripSQLComments(string(raw)), "\n") {
			if !reLikePattern.MatchString(line) {
				continue
			}
			found++
			if !strings.Contains(strings.ToUpper(line), `ESCAPE '\'`) {
				t.Errorf("%s: a LIKE/ILIKE against a parameter has no ESCAPE '\\' clause, so "+
					"escapeLike's backslashes are literal characters and %% / _ still match as "+
					"wildcards.\n\n  %s", path, strings.TrimSpace(line))
			}
		}
	}
	// A guard that silently matches nothing is worse than no guard: it reads
	// green forever after a refactor renames the shape it looks for.
	if found == 0 {
		t.Fatal("no parameterised LIKE/ILIKE found in internal/db/queries; " +
			"this guard is no longer looking at anything real")
	}
}

// TestLikeEscapeGuardRefusesAViolation plants the exact defect and asserts the
// matcher catches it, so the guard cannot pass by looking at the wrong thing.
func TestLikeEscapeGuardRefusesAViolation(t *testing.T) {
	violation := `SELECT * FROM logs WHERE body ILIKE '%' || sqlc.narg(q) || '%'`
	if !reLikePattern.MatchString(violation) {
		t.Fatal("the matcher does not recognise the shape it exists to catch")
	}
	if strings.Contains(strings.ToUpper(violation), `ESCAPE '\'`) {
		t.Fatal("the planted violation was not a violation")
	}
	fixed := violation + ` ESCAPE '\'`
	if !strings.Contains(strings.ToUpper(fixed), `ESCAPE '\'`) {
		t.Fatal("the fixed form is not recognised as fixed")
	}
}

// stripSQLComments removes -- line comments so a commented-out example cannot
// trip the guard, and so an explanatory comment mentioning ESCAPE cannot
// satisfy it.
func stripSQLComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
