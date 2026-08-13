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

// POST /api/auth/accept-invite was the last unlimited unauthenticated POST once
// register was gated (audit L16). It is not register's hole, because the secret
// token means an attacker holding none creates no rows, but every request buys
// an unauthenticated Postgres lookup and a request with a token buys a bcrypt at
// cost 12. These tests hold the three properties that make the limit real: it
// trips, it trips before the body is parsed, and the caller cannot mint a fresh
// budget out of the body.
//
// Same constraint as register_gate_test.go: no Postgres is reachable in this
// suite, so these reuse deadPool from that file. Every query fails fast, which
// is fine here because none of these assertions depends on a query SUCCEEDING.
// What they cannot cover is the accepted-invite happy path, which needs a real
// server; see flare/AUDIT-2026-08-11.md, coverage boundary.

// inviteAcceptBudget mirrors the ceiling wired in NewServer. Kept in sync by
// hand, like registerServer's, so a test cannot silently follow a widened limit.
const inviteAcceptBudget = 10

func acceptInviteServer(t *testing.T) *Server {
	t.Helper()
	pool := deadPool(t)
	return &Server{
		q:             generated.New(pool),
		pool:          pool,
		inviteLimiter: ratelimit.New(inviteAcceptBudget, 15*time.Minute),
	}
}

func acceptInviteReq(body, ip string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/auth/accept-invite", strings.NewReader(body))
	r.RemoteAddr = ip + ":54321"
	return r
}

// acceptBody is a well-formed request: valid JSON, password long enough to pass
// the length check, so the handler runs as far as the token lookup.
func acceptBody(token string) string {
	return `{"token":"` + token + `","password":"aaaaaaaa"}`
}

// TestAcceptInviteIsRateLimitedPerIP is the plain closure. Login, password reset
// and register are all limited; the fourth unauthenticated route, the one that
// runs bcrypt, was not.
func TestAcceptInviteIsRateLimitedPerIP(t *testing.T) {
	s := acceptInviteServer(t)

	const attacker = "203.0.113.20"
	for i := 1; i <= inviteAcceptBudget; i++ {
		w := httptest.NewRecorder()
		s.handleAcceptInvite(w, acceptInviteReq(acceptBody("nope"), attacker))
		if w.Code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d was limited, the budget of %d is not being honoured", i, inviteAcceptBudget)
		}
	}

	w := httptest.NewRecorder()
	s.handleAcceptInvite(w, acceptInviteReq(acceptBody("nope"), attacker))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt %d from one IP should be 429, got %d (body=%s)",
			inviteAcceptBudget+1, w.Code, w.Body.String())
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("a 429 without Retry-After tells the invitee nothing about when to come back")
	}

	// A different address must not inherit the attacker's exhausted budget, or
	// one attacker locks a whole team out of joining a workspace.
	w = httptest.NewRecorder()
	s.handleAcceptInvite(w, acceptInviteReq(acceptBody("nope"), "203.0.113.21"))
	if w.Code == http.StatusTooManyRequests {
		t.Error("a second IP was limited by the first IP's budget: the bucket is not keyed per address")
	}
}

// TestAcceptInviteRateLimitPrecedesBodyParsing pins the ordering. A limit that
// sits after decodeJSON is not a limit: unparseable bodies are the cheapest
// thing to send, they pay nothing, and the budget is never consumed.
func TestAcceptInviteRateLimitPrecedesBodyParsing(t *testing.T) {
	s := acceptInviteServer(t)

	const ip = "203.0.113.22"
	for i := 1; i <= inviteAcceptBudget; i++ {
		w := httptest.NewRecorder()
		s.handleAcceptInvite(w, acceptInviteReq("not json at all", ip))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("attempt %d: unparseable body should be 400, got %d", i, w.Code)
		}
	}

	w := httptest.NewRecorder()
	s.handleAcceptInvite(w, acceptInviteReq("not json at all", ip))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("unparseable bodies must still consume the budget, got %d on attempt %d",
			w.Code, inviteAcceptBudget+1)
	}
}

// TestAcceptInviteBudgetIgnoresWhatTheCallerSends is the bounded-key closure,
// and it is two properties in one assertion.
//
// A key with a caller-chosen component is not a ceiling: change the component,
// get a fresh budget. So varying the token across requests must NOT buy more
// attempts, whether the token goes into the key raw or hashed through
// limiterEmailKey.
//
// Raw is also the audit's only CRITICAL (L4#1) at a new call site: the entry is
// created before any auth or DB work and held for the whole window, so eleven
// 256 KiB tokens would retain ~2.8 MiB from eleven unauthenticated requests,
// and a real attacker sends rather more than eleven. The tokens here are
// deliberately large AND distinct so one test covers both halves.
func TestAcceptInviteBudgetIgnoresWhatTheCallerSends(t *testing.T) {
	s := acceptInviteServer(t)

	// Well under decodeJSON's 1 MiB cap, so the body genuinely parses and an
	// ablated key really would see the token, rather than the size check
	// rejecting it first and making this test pass for the wrong reason.
	const ip = "203.0.113.23"
	fat := func(i int) string {
		return acceptBody(strings.Repeat("t", 256<<10) + string(rune('a'+i)))
	}

	for i := 1; i <= inviteAcceptBudget; i++ {
		w := httptest.NewRecorder()
		s.handleAcceptInvite(w, acceptInviteReq(fat(i), ip))
		if w.Code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d was limited, the budget of %d is not being honoured", i, inviteAcceptBudget)
		}
	}

	w := httptest.NewRecorder()
	s.handleAcceptInvite(w, acceptInviteReq(fat(inviteAcceptBudget+1), ip))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("a caller bought a fresh budget by changing the token: attempt %d returned %d, want 429. "+
			"The limiter key must not carry anything from the request body",
			inviteAcceptBudget+1, w.Code)
	}
}

// TestAcceptInviteRefusalDoesNotVaryByRequest holds the last property: the one
// response an unauthenticated caller can always reach must say nothing about the
// workspace, the invitee or the address it was minted for.
func TestAcceptInviteRefusalDoesNotVaryByRequest(t *testing.T) {
	s := acceptInviteServer(t)

	exhaust := func(ip, token string) *httptest.ResponseRecorder {
		t.Helper()
		var w *httptest.ResponseRecorder
		for i := 0; i <= inviteAcceptBudget; i++ {
			w = httptest.NewRecorder()
			s.handleAcceptInvite(w, acceptInviteReq(acceptBody(token), ip))
		}
		if w.Code != http.StatusTooManyRequests {
			t.Fatalf("%s: expected the budget to be spent, got %d", ip, w.Code)
		}
		return w
	}

	a := exhaust("198.51.100.20", "alice-invite-token")
	b := exhaust("198.51.100.21", "bob-invite-token")

	if a.Body.String() != b.Body.String() {
		t.Errorf("the refusal differs by request: %q vs %q", a.Body.String(), b.Body.String())
	}
	for _, leak := range []string{"alice", "bob", "invite-token"} {
		if strings.Contains(a.Body.String(), leak) {
			t.Errorf("the refusal echoes caller input %q back: %s", leak, a.Body.String())
		}
	}
}
