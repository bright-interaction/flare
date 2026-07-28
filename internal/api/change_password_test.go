package api

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestChangePasswordRequiresTheCurrentPassword pins the property that makes an
// authenticated change-password endpoint safe to expose.
//
// A signed-in session is NOT sufficient authority to change the credential that
// session rests on. If it were, a borrowed laptop or a stolen cookie would
// convert into a permanent account takeover, which is strictly worse than the
// gap this endpoint fills.
//
// Source-level because the handler needs a live session, a database and a real
// argon2 hash to drive end to end; the property here is about which checks exist
// and in what order.
func TestChangePasswordRequiresTheCurrentPassword(t *testing.T) {
	src, err := os.ReadFile("auth_handlers.go")
	if err != nil {
		t.Fatalf("read auth_handlers.go: %v", err)
	}
	body := string(src)
	i := strings.Index(body, "func (s *Server) handleChangePassword(")
	if i < 0 {
		t.Fatal("handleChangePassword not found: there is no authenticated way to change a password")
	}
	fn := body[i:]
	if j := regexp.MustCompile(`(?m)^func `).FindStringIndex(fn[1:]); j != nil {
		fn = fn[:j[0]+1]
	}

	if !strings.Contains(fn, "VerifyPasswordConstantTime") {
		t.Error("handleChangePassword does not verify the CURRENT password. A stolen session " +
			"would then be enough to take the account permanently.")
	}
	// The verification must come before the write, or the check is decorative.
	verify := strings.Index(fn, "VerifyPasswordConstantTime")
	write := strings.Index(fn, "UpdateUserPassword")
	if verify < 0 || write < 0 || verify > write {
		t.Error("the current-password check does not precede UpdateUserPassword")
	}
	// Brute-forcing the current password from inside a session must be bounded.
	if !strings.Contains(fn, "loginLimiter.Blocked") || !strings.Contains(fn, "loginLimiter.Record") {
		t.Error("failed attempts are not rate-limited; a stolen cookie could brute-force the " +
			"current password from inside the session")
	}
	// Other sessions must go, or "change your password" does not evict whoever
	// prompted the change.
	if !strings.Contains(fn, "BumpUserSessionsValidFrom") {
		t.Error("other sessions are not revoked on a password change")
	}
	// ...but this one must survive, or the user is thrown out of the page they
	// are standing on and cannot tell success from failure.
	if !strings.Contains(fn, "establishSession") {
		t.Error("the current session is not re-established, so a successful change logs the user out")
	}
}

// TestForgotPasswordDoesNotClaimToSendMailItCannot covers the other half.
//
// The generic "if that address exists, a link is on its way" answer is correct
// when mail works: it stops account enumeration. With SMTP unconfigured no link
// can ever arrive, so the same words become a lie, and the user waits for an
// email that does not exist with no way to distinguish that from a typo.
//
// Saying so leaks nothing: it is a property of the INSTANCE, identical for every
// address.
func TestForgotPasswordDoesNotClaimToSendMailItCannot(t *testing.T) {
	src, err := os.ReadFile("auth_handlers.go")
	if err != nil {
		t.Fatalf("read auth_handlers.go: %v", err)
	}
	body := string(src)
	i := strings.Index(body, "func (s *Server) handleForgotPassword(")
	if i < 0 {
		t.Fatal("handleForgotPassword not found")
	}
	fn := body[i:]
	if j := regexp.MustCompile(`(?m)^func `).FindStringIndex(fn[1:]); j != nil {
		fn = fn[:j[0]+1]
	}

	k := strings.Index(fn, "mailer.Enabled()")
	if k < 0 {
		t.Fatal("handleForgotPassword no longer checks whether mail is configured")
	}
	branch := fn[k:]
	if end := strings.Index(branch, "\n\t}"); end > 0 {
		branch = branch[:end]
	}
	if regexp.MustCompile(`\bok\(\)`).MatchString(branch) {
		t.Error("with SMTP unconfigured, forgot-password still answers with the generic " +
			"success response, telling the user a link was sent that can never arrive")
	}
	if !strings.Contains(branch, "StatusServiceUnavailable") {
		t.Error("the SMTP-unconfigured branch should say plainly that reset by email is " +
			"unavailable on this instance")
	}
}
