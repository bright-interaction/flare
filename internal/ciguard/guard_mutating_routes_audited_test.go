package ciguard

// Structural backstop for the "some mutations are recorded and some are not"
// family (audit finding M19, 2026-08-11).
//
// The audit swept all 21 s.audit call sites against every mutating route and
// found the coverage was arbitrary: channel.update was audited, channel.create
// and channel.delete were not. That is the wrong way round. A compromised
// member creating a webhook channel pointed at their own endpoint mirrors every
// alert in the org, including the sensitive-data-in-payload security events,
// and the audit log an admin reads at GET /api/audit-log showed nothing.
// Deleting it afterwards was equally invisible. The one operation on that
// resource that WAS recorded is the least sensitive one.
//
// So the rule is enforced rather than remembered: a non-GET route registered
// inside a requireRole group either calls s.audit, or appears in the exemption
// table below with a reason. Adding a route makes the build tell you which.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// exemptFromAudit lists mutating routes that deliberately write no audit row.
// A new entry needs a reason that survives being read out loud in an incident.
var exemptFromAudit = map[string]string{
	"handleUpdateIssueStatus": "issue workflow state, changed constantly during triage; the issue itself carries its status and history",
	"handleTriageIssue":       "audited one level down, inside triageIssue, so all three triage surfaces record through one call",
	"handleCreateRelease":     "written by CI on every deploy; volume would drown the log an admin reads",
	"handleDeleteOrg":         "audit_log is FK-linked to orgs, so the cascade deletes the evidence; recorded as a slog line instead",
	"handleLogout":            "ends the caller's own session, no state another member can observe",
}

var (
	reRequireRole = regexp.MustCompile(`requireRole\("(\w+)"\)`)
	reMutating    = regexp.MustCompile(`r\.(Post|Put|Patch|Delete)\("[^"]*",\s*s\.(\w+)\)`)
	reHandlerFunc = regexp.MustCompile(`func \(s \*Server\) (\w+)\(w http\.ResponseWriter, r \*http\.Request\)`)
)

func TestMutatingRoleGatedRoutesAreAudited(t *testing.T) {
	router, err := os.ReadFile("../api/router.go")
	if err != nil {
		t.Fatalf("read router.go: %v", err)
	}
	// Everything from the first requireRole group onward is role-gated. The
	// unauthenticated and ingest routes sit above it and are not in scope: they
	// are authenticated by a DSN key or by nothing, so there is no actor to
	// attribute an action to.
	src := string(router)
	first := reRequireRole.FindStringIndex(src)
	if first == nil {
		t.Fatal("no requireRole group found in router.go; this guard needs rewiring")
	}
	handlers := map[string]bool{}
	for _, m := range reMutating.FindAllStringSubmatch(src[first[0]:], -1) {
		handlers[m[2]] = true
	}
	if len(handlers) == 0 {
		t.Fatal("no mutating role-gated routes found; this guard is no longer looking at anything real")
	}

	bodies, err := handlerBodies("../api")
	if err != nil {
		t.Fatalf("read handlers: %v", err)
	}
	for name := range handlers {
		if reason, ok := exemptFromAudit[name]; ok {
			if reason == "" {
				t.Errorf("%s is exempt from auditing with no reason given", name)
			}
			continue
		}
		body, ok := bodies[name]
		if !ok {
			t.Errorf("%s is routed but its definition was not found; this guard cannot see it", name)
			continue
		}
		if !strings.Contains(body, "s.audit(") {
			t.Errorf("%s mutates inside a requireRole group and writes no audit row.\n"+
				"Add s.audit(ctx, \"<resource>.<verb>\", target), or add it to exemptFromAudit "+
				"with a reason.", name)
		}
	}
}

// TestAuditExemptionsAreStillRouted keeps the exemption table from rotting: an
// entry for a handler that no longer exists is a comment pretending to be a
// control.
func TestAuditExemptionsAreStillRouted(t *testing.T) {
	bodies, err := handlerBodies("../api")
	if err != nil {
		t.Fatalf("read handlers: %v", err)
	}
	for name := range exemptFromAudit {
		if _, ok := bodies[name]; !ok {
			t.Errorf("exemptFromAudit lists %s, which no longer exists", name)
		}
	}
}

// handlerBodies maps handler name -> source text, split on the next top-level
// func so a body cannot borrow its neighbour's audit call.
func handlerBodies(dir string) (map[string]string, error) {
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(dir + "/" + e.Name())
		if err != nil {
			return nil, err
		}
		src := string(raw)
		locs := reHandlerFunc.FindAllStringSubmatchIndex(src, -1)
		for i, loc := range locs {
			name := src[loc[2]:loc[3]]
			end := len(src)
			if i+1 < len(locs) {
				end = locs[i+1][0]
			}
			out[name] = src[loc[0]:end]
		}
	}
	return out, nil
}
