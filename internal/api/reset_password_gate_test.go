package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bright-interaction/flare/internal/db/generated"
	"github.com/bright-interaction/flare/internal/ratelimit"
)

// POST /api/auth/reset-password had no rate limiter of any kind, while
// /auth/forgot-password, the other half of the same flow, has had one since it
// shipped. The mint was capped and the spend was not, and the spend is the half
// that runs bcrypt at cost 12.
//
// It is also not the one-shot it looks like. handleResetPassword opens no
// transaction and its token SELECT takes no lock, so the entire bcrypt runs
// inside the window between reading the token and invalidating it: concurrent
// replays of ONE forwarded link all pass the lookup and all pay for a hash.
//
// These tests hold the same three properties the accept-invite gate holds: it
// trips, it trips before the body is parsed, and the caller cannot mint a fresh
// budget out of the body.
//
// Same constraint as register_gate_test.go and accept_invite_gate_test.go: no
// Postgres is reachable in this suite, so these reuse deadPool. Every query
// fails fast, which is fine because no assertion here depends on a query
// SUCCEEDING. What they cannot cover is the completed-reset happy path and the
// concurrent-replay amplification itself, both of which need a real server and a
// live token; see flare/AUDIT-2026-08-11.md, coverage boundary.

// resetConsumeBudget mirrors the ceiling wired in NewServer. Kept in sync by
// hand, like registerServer's and acceptInviteServer's, so a test cannot
// silently follow a widened limit.
const resetConsumeBudget = 10

func resetPasswordServer(t *testing.T) *Server {
	t.Helper()
	pool := deadPool(t)
	return &Server{
		q:                   generated.New(pool),
		pool:                pool,
		resetConsumeLimiter: ratelimit.New(resetConsumeBudget, 15*time.Minute),
	}
}

func resetPasswordReq(body, ip string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/auth/reset-password", strings.NewReader(body))
	r.RemoteAddr = ip + ":54321"
	return r
}

// resetBody is a well-formed request: valid JSON, password long enough to clear
// the length check, so the handler runs as far as the token lookup.
func resetBody(token string) string {
	return `{"token":"` + token + `","password":"aaaaaaaa"}`
}

// TestResetPasswordIsRateLimitedPerIP is the plain closure. Login, register,
// accept-invite and forgot-password are all limited; the step that actually
// consumes the reset token, and runs the bcrypt, was not.
func TestResetPasswordIsRateLimitedPerIP(t *testing.T) {
	s := resetPasswordServer(t)

	const attacker = "203.0.113.30"
	for i := 1; i <= resetConsumeBudget; i++ {
		w := httptest.NewRecorder()
		s.handleResetPassword(w, resetPasswordReq(resetBody("nope"), attacker))
		if w.Code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d was limited, the budget of %d is not being honoured", i, resetConsumeBudget)
		}
	}

	w := httptest.NewRecorder()
	s.handleResetPassword(w, resetPasswordReq(resetBody("nope"), attacker))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt %d from one IP should be 429, got %d (body=%s)",
			resetConsumeBudget+1, w.Code, w.Body.String())
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("a 429 without Retry-After tells the user nothing about when to come back")
	}

	// A different address must not inherit the attacker's exhausted budget, or
	// one attacker locks everyone else out of recovering their account.
	w = httptest.NewRecorder()
	s.handleResetPassword(w, resetPasswordReq(resetBody("nope"), "203.0.113.31"))
	if w.Code == http.StatusTooManyRequests {
		t.Error("a second IP was limited by the first IP's budget: the bucket is not keyed per address")
	}
}

// TestResetPasswordRateLimitPrecedesBodyParsing pins the ordering. A limit that
// sits after decodeJSON is not a limit: unparseable bodies are the cheapest
// thing to send, they pay nothing, and the budget is never consumed.
//
// This is the ordering /forgot-password cannot have, because its key needs the
// email out of the body. This route carries no address, so nothing forces the
// weaker ordering here and the limiter belongs in front.
func TestResetPasswordRateLimitPrecedesBodyParsing(t *testing.T) {
	s := resetPasswordServer(t)

	const ip = "203.0.113.32"
	for i := 1; i <= resetConsumeBudget; i++ {
		w := httptest.NewRecorder()
		s.handleResetPassword(w, resetPasswordReq("not json at all", ip))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("attempt %d: unparseable body should be 400, got %d", i, w.Code)
		}
	}

	w := httptest.NewRecorder()
	s.handleResetPassword(w, resetPasswordReq("not json at all", ip))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("unparseable bodies must still consume the budget, got %d on attempt %d",
			w.Code, resetConsumeBudget+1)
	}
}

// TestResetPasswordBudgetIgnoresWhatTheCallerSends is the bounded-key closure,
// and like its accept-invite twin it is two properties in one assertion.
//
// A key with a caller-chosen component is not a ceiling: change the component,
// get a fresh budget. So varying the token across requests must NOT buy more
// attempts, whether the token goes into the key raw or hashed through
// limiterEmailKey. This is the specific trap on this route, because the obvious
// "reuse resetLimiter" shortcut keys on a caller-supplied value.
//
// Raw is also the audit's only CRITICAL (L4#1) at a new call site: the entry is
// created before any auth or DB work and held for the whole window, so eleven
// 256 KiB tokens would retain ~2.8 MiB from eleven unauthenticated requests. The
// tokens here are deliberately large AND distinct so one test covers both
// halves.
func TestResetPasswordBudgetIgnoresWhatTheCallerSends(t *testing.T) {
	s := resetPasswordServer(t)

	// Well under decodeJSON's 1 MiB cap, so the body genuinely parses and an
	// ablated key really would see the token, rather than the size check
	// rejecting it first and making this test pass for the wrong reason.
	const ip = "203.0.113.33"
	fat := func(i int) string {
		return resetBody(strings.Repeat("t", 256<<10) + string(rune('a'+i)))
	}

	for i := 1; i <= resetConsumeBudget; i++ {
		w := httptest.NewRecorder()
		s.handleResetPassword(w, resetPasswordReq(fat(i), ip))
		if w.Code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d was limited, the budget of %d is not being honoured", i, resetConsumeBudget)
		}
	}

	w := httptest.NewRecorder()
	s.handleResetPassword(w, resetPasswordReq(fat(resetConsumeBudget+1), ip))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("a distinct token bought a fresh budget: attempt %d should be 429, got %d. "+
			"The token is in the limiter key, so the caller sets their own ceiling",
			resetConsumeBudget+1, w.Code)
	}
}
