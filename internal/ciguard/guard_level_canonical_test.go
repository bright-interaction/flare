package ciguard

// Structural backstop for the "one concept normalised in some places and not
// others" family (audit finding H5, 2026-08-11).
//
// A level is compared in three places that must agree:
//
//   - ingest.canonicalLevel, which decides what is STORED
//   - api.pageableLevel, the Go gate that decides whether an issue may page
//   - ten SQL predicates spelling `level NOT IN ('info', 'debug')`, which decide
//     what counts as an error
//
// They did not agree. The Go gate lowercased and trimmed; the SQL compared raw
// and case-sensitively. So "INFO", which is what an OTLP-conventional service
// emits, was informational to the pager and actionable to every counter.
// CountActionableEventsForProjectSince exists BECAUSE the whole estate was
// paged on heartbeat volume on 2026-07-11, and an uppercase level reproduces
// that incident exactly.
//
// Two properties are enforced here. The SQL literals must be lowercase, since
// the stored value now always is. And the informational set must be the SAME
// set everywhere: adding "trace" to the Go gate without adding it to the
// predicates is the next instance of this bug, so it fails the build.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// informationalLevels is the declared set. api.pageableLevel and every SQL
// predicate must match it exactly. Change this and the build tells you every
// place that has to change with it.
var informationalLevels = []string{"debug", "info"}

var reLevelPredicate = regexp.MustCompile(`(?i)\blevel\s+NOT\s+IN\s*\(([^)]*)\)`)

func TestLevelPredicatesUseTheDeclaredInformationalSet(t *testing.T) {
	dir := "../db/queries"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read queries dir: %v", err)
	}
	want := strings.Join(informationalLevels, ",")
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
		for _, m := range reLevelPredicate.FindAllStringSubmatch(stripSQLComments(string(raw)), -1) {
			found++
			got := parseSQLStringList(m[1])
			if strings.Join(got, ",") != want {
				t.Errorf("%s: level predicate lists %v, but the declared informational set is %v.\n"+
					"Every surface that decides 'is this an error' has to use the same set, or "+
					"the pager and the counters disagree about the same row.\n\n  %s",
					path, got, informationalLevels, strings.TrimSpace(m[0]))
			}
			for _, lit := range got {
				if lit != strings.ToLower(lit) {
					t.Errorf("%s: level predicate compares against %q. Levels are canonicalised to "+
						"lowercase at ingest, so an uppercase literal can never match.", path, lit)
				}
			}
		}
	}
	if found == 0 {
		t.Fatal("no `level NOT IN (...)` predicate found in internal/db/queries; " +
			"this guard is no longer looking at anything real")
	}
}

// TestGoLevelGateMatchesTheDeclaredSet reads the Go gate's own source rather
// than calling it, so the two definitions cannot drift apart silently: a level
// added to the switch without being added here fails the build.
func TestGoLevelGateMatchesTheDeclaredSet(t *testing.T) {
	raw, err := os.ReadFile("../api/ingest_handlers.go")
	if err != nil {
		t.Fatalf("read ingest_handlers.go: %v", err)
	}
	src := string(raw)
	i := strings.Index(src, "func pageableLevel(")
	if i < 0 {
		t.Fatal("pageableLevel is gone; this guard needs rewiring to whatever replaced it")
	}
	body := src[i:]
	if j := strings.Index(body, "\n}"); j > 0 {
		body = body[:j]
	}
	k := strings.Index(body, "case ")
	if k < 0 {
		t.Fatalf("pageableLevel no longer has a case list:\n%s", body)
	}
	line := body[k+len("case "):]
	if j := strings.IndexByte(line, ':'); j > 0 {
		line = line[:j]
	}
	got := parseGoStringList(line)
	if strings.Join(got, ",") != strings.Join(informationalLevels, ",") {
		t.Errorf("pageableLevel treats %v as informational, the declared set is %v. "+
			"The Go pager gate and the SQL counters must agree about the same row.",
			got, informationalLevels)
	}
}

func parseSQLStringList(s string) []string { return parseQuoted(s, '\'') }
func parseGoStringList(s string) []string  { return parseQuoted(s, '"') }

func parseQuoted(s string, quote byte) []string {
	var out []string
	for {
		i := strings.IndexByte(s, quote)
		if i < 0 {
			break
		}
		s = s[i+1:]
		j := strings.IndexByte(s, quote)
		if j < 0 {
			break
		}
		out = append(out, s[:j])
		s = s[j+1:]
	}
	sort.Strings(out)
	return out
}
