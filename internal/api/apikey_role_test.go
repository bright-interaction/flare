package api

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestAPIKeyRoleClamp locks the authority an API key row is allowed to carry.
//
// requireAuth used to stamp ctxRole = "member" for ANY valid key. That made
// every key a write key: a token handed to a status page, a dashboard, or a
// read-only MCP reader could call DELETE /projects/{id}, which erases a project
// and all of its telemetry, and could mint and revoke API keys. There was no
// way to issue a narrower key and no way to tell from a key what it could do.
//
// The rule now: honour exactly the two mintable roles, and treat everything
// else as read-only. "Everything else" is not hypothetical. Rows created before
// migration 024 have no role, a hand-edited row could say "owner", and a future
// migration could add a role this build has never heard of. On every one of
// those the safe direction is down.
func TestAPIKeyRoleClamp(t *testing.T) {
	for _, tc := range []struct {
		stored string
		want   string
		why    string
	}{
		{"viewer", "viewer", "the mintable read-only role is honoured"},
		{"member", "member", "the mintable write role is honoured"},
		{"", "viewer", "a row predating the role column must not get write access"},
		{"owner", "viewer", "a key must never reach team management, however the row got written"},
		{"admin", "viewer", "same: admin gates SSO config and workspace deletion"},
		{"Member", "viewer", "roles are compared exactly; no case-folding escape hatch"},
		{" member ", "viewer", "no whitespace-padded variant slips through"},
		{"superuser", "viewer", "an unknown future role fails closed"},
	} {
		if got := apiKeyRole(tc.stored); got != tc.want {
			t.Errorf("apiKeyRole(%q) = %q, want %q (%s)", tc.stored, got, tc.want, tc.why)
		}
	}
}

// TestAPIKeyRoleIsNotHardcoded is the regression guard for the original bug.
//
// The clamp above is only worth anything if requireAuth actually reads the role
// off the key. The defect was one line, `ctx = context.WithValue(ctx, ctxRole,
// "member")`, and it looked completely reasonable in review, so this asserts on
// the source: no literal role may be stamped into the request context anywhere
// in the auth middleware. Roles come from a user row or a key row, never from a
// constant.
func TestAPIKeyRoleIsNotHardcoded(t *testing.T) {
	src, err := os.ReadFile("middleware.go")
	if err != nil {
		t.Fatalf("read middleware.go: %v", err)
	}
	literalRole := regexp.MustCompile(`WithValue\([^)]*ctxRole,\s*"`)
	if loc := literalRole.FindIndex(src); loc != nil {
		line := 1 + strings.Count(string(src[:loc[0]]), "\n")
		t.Errorf("middleware.go:%d stamps a hardcoded role into the request context. "+
			"Derive it from the authenticated principal instead: a constant here gives every "+
			"credential the same authority, which is exactly how read-only API keys ended up "+
			"able to delete projects.", line)
	}
}
