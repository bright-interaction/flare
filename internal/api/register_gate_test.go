package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bright-interaction/flare/internal/config"
	"github.com/bright-interaction/flare/internal/db/generated"
	"github.com/bright-interaction/flare/internal/ratelimit"
)

// POST /api/auth/register is the only unauthenticated route that writes rows. It
// shipped with no bootstrap gate, no rate limit, and CreateOrg/CreateUser as two
// separate statements, on every deployment including a live one carrying 21
// projects. These tests hold each of the three closures.
//
// No Postgres is reachable in this suite, so the gate is proven by ORDERING
// rather than by outcome: a pool pointed at a dead address makes every query
// fail, and slogError writes a distinct label per call site, so the label of the
// first failure names which query the handler reached first. That is exactly the
// property at stake. What these tests cannot cover is the 403 body itself and
// the transaction rollback, both of which need a real server; see
// flare/AUDIT-2026-08-11.md, coverage boundary.

// capturingLogHandler records slog messages so a test can assert which database
// call a handler made first.
type capturingLogHandler struct {
	mu   sync.Mutex
	msgs []string
}

func (h *capturingLogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *capturingLogHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgs = append(h.msgs, r.Message)
	return nil
}

func (h *capturingLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *capturingLogHandler) WithGroup(string) slog.Handler      { return h }

func (h *capturingLogHandler) first() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.msgs) == 0 {
		return ""
	}
	return h.msgs[0]
}

func captureLogs(t *testing.T) *capturingLogHandler {
	t.Helper()
	h := &capturingLogHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

// deadPool is a pool whose every query fails on acquire, fast. pgxpool.New does
// not dial eagerly, so this construction never blocks.
func deadPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(),
		"postgres://flare:flare@127.0.0.1:1/flare?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("build dead pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func registerServer(t *testing.T, allowSignup bool) *Server {
	t.Helper()
	pool := deadPool(t)
	return &Server{
		q:             generated.New(pool),
		pool:          pool,
		cfg:           config.Config{AllowSignup: allowSignup},
		signupLimiter: ratelimit.New(5, time.Hour),
	}
}

func registerReq(body, ip string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(body))
	r.RemoteAddr = ip + ":54321"
	return r
}

const validRegisterBody = `{"email":"first@example.com","password":"aaaaaaaa","org_name":"Acme"}`

// TestRegisterConsultsTheBootstrapGateBeforeAnyEmailLookup is the ablation test
// for the gate. CountUsers must be the first query the handler reaches, before
// GetUserByEmail, because the email lookup is what produced the 409-versus-201
// enumeration oracle that handleLogin and handleForgotPassword were both
// deliberately hardened against.
func TestRegisterConsultsTheBootstrapGateBeforeAnyEmailLookup(t *testing.T) {
	logs := captureLogs(t)
	s := registerServer(t, false)

	w := httptest.NewRecorder()
	s.handleRegister(w, registerReq(validRegisterBody, "198.51.100.7"))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("dead pool should surface as 500, got %d (body=%s)", w.Code, w.Body.String())
	}
	if got := logs.first(); got != "count users" {
		t.Fatalf("first database call was %q, want %q: the bootstrap gate is not running before the email lookup", got, "count users")
	}
}

// TestRegisterSkipsTheGateWhenSignupIsExplicitlyAllowed proves the flag is load
// bearing rather than decorative. With FLARE_ALLOW_SIGNUP on, CountUsers must
// not run at all, so the first failure is the email lookup instead.
func TestRegisterSkipsTheGateWhenSignupIsExplicitlyAllowed(t *testing.T) {
	logs := captureLogs(t)
	s := registerServer(t, true)

	w := httptest.NewRecorder()
	s.handleRegister(w, registerReq(validRegisterBody, "198.51.100.8"))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("dead pool should surface as 500, got %d", w.Code)
	}
	if got := logs.first(); got != "lookup user" {
		t.Fatalf("first database call was %q, want %q: AllowSignup did not bypass the gate", got, "lookup user")
	}
}

// TestRegisterIsRateLimitedPerIP holds the second closure. Login and password
// reset were both limited; the route that creates an org and an owner was not,
// so it could be looped to mint orgs, each carrying its own ingest budget.
func TestRegisterIsRateLimitedPerIP(t *testing.T) {
	s := registerServer(t, false)

	const attacker = "203.0.113.9"
	for i := 1; i <= 5; i++ {
		w := httptest.NewRecorder()
		s.handleRegister(w, registerReq("{", attacker))
		if w.Code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d was limited, the budget of 5 is not being honoured", i)
		}
	}

	w := httptest.NewRecorder()
	s.handleRegister(w, registerReq("{", attacker))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt from one IP should be 429, got %d", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("a 429 without Retry-After tells the caller nothing about when to return")
	}

	// A different address must not inherit the attacker's exhausted budget, or
	// one attacker locks every other operator out of first-run registration.
	w = httptest.NewRecorder()
	s.handleRegister(w, registerReq("{", "203.0.113.10"))
	if w.Code == http.StatusTooManyRequests {
		t.Error("a second IP was limited by the first IP's budget: the bucket is not keyed per address")
	}
}

// TestRegisterRateLimitPrecedesBodyParsing pins the ordering. A limit applied
// after decodeJSON is not a limit: an attacker sends unparseable bodies, pays
// nothing, and the budget is never consumed.
func TestRegisterRateLimitPrecedesBodyParsing(t *testing.T) {
	s := registerServer(t, false)

	const ip = "203.0.113.11"
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		s.handleRegister(w, registerReq("not json at all", ip))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("attempt %d: unparseable body should be 400, got %d", i+1, w.Code)
		}
	}

	w := httptest.NewRecorder()
	s.handleRegister(w, registerReq("not json at all", ip))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("unparseable bodies must still consume the budget, got %d on the 6th", w.Code)
	}
}
