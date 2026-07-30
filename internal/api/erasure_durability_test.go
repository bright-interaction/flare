package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/bright-interaction/flare/internal/db/generated"
	"github.com/bright-interaction/flare/internal/id"
)

// TestRecordPartialErasureSurvivesCancelledRequest is the D2 proof.
//
// recordPartialErasure runs AFTER the hot-tier delete has committed, on the
// request context (r.Context()). A client disconnect or proxy timeout at that
// instant cancels the context, and on the pre-fix code the RecordPendingErasure
// insert then fails with "context canceled" - losing the only durable trace of
// an unfulfilled erasure, which for an org delete is unrecoverable. The fix
// detaches the context inside recordPartialErasure, so the row is still written.
//
// This test passes an ALREADY-cancelled context. With the detach it writes the
// row; revert the WithoutCancel line and it goes red (the row never lands).
func TestRecordPartialErasureSurvivesCancelledRequest(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()

	org := id.New()
	if _, err := pool.Exec(ctx, "INSERT INTO orgs (id, name, slug) VALUES ($1, $2, $3)", org, "Acme", "acme-"+org); err != nil {
		t.Fatalf("seed org: %v", err)
	}

	s := serverForDB(pool)

	// The context the handler would hold at the moment of a client disconnect:
	// already cancelled before the durability write runs.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	s.recordPartialErasure(cancelled, org, "", "org_id", org, "user-1", "cold tier not supported on s3")

	// The obligation must be durable despite the cancel.
	rows, err := s.q.ListOpenErasuresByOrg(ctx, org)
	if err != nil {
		t.Fatalf("list erasures: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("cancelled request dropped the erasure obligation: want 1 durable row, got %d "+
			"(the detach in recordPartialErasure did not take)", len(rows))
	}
	if rows[0].ScopeColumn != "org_id" || rows[0].ScopeValue != org {
		t.Fatalf("wrong obligation persisted: scope=%s=%s", rows[0].ScopeColumn, rows[0].ScopeValue)
	}
}

// TestCompletePendingErasureClearsTheRow is the D4(b) proof: completion sets
// completed_at and the row drops out of the open lists (both the org-scoped and
// the operator-wide view). Before this fix no query set completed_at at all.
func TestCompletePendingErasureClearsTheRow(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()

	org := id.New()
	if _, err := pool.Exec(ctx, "INSERT INTO orgs (id, name, slug) VALUES ($1, $2, $3)", org, "Acme", "acme-"+org); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	s := serverForDB(pool)
	s.recordPartialErasure(ctx, org, "", "org_id", org, "user-1", "s3 cold tier")

	open, _ := s.q.ListOpenErasuresByOrg(ctx, org)
	if len(open) != 1 {
		t.Fatalf("setup: want 1 open obligation, got %d", len(open))
	}
	rowID := open[0].ID

	n, err := s.q.CompletePendingErasure(ctx, generated.CompletePendingErasureParams{
		ID: rowID, OrgID: org, CompletedBy: pgText("operator-9"),
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if n != 1 {
		t.Fatalf("want 1 row completed, got %d", n)
	}

	// Drops out of the org-scoped list AND the operator-wide list/count.
	if got, _ := s.q.ListOpenErasuresByOrg(ctx, org); len(got) != 0 {
		t.Fatalf("completed obligation still open in org list: %d", len(got))
	}
	if got, _ := s.q.ListOpenErasures(ctx); len(got) != 0 {
		t.Fatalf("completed obligation still in operator-wide list: %d", len(got))
	}
	if c, _ := s.q.CountOpenErasures(ctx); c != 0 {
		t.Fatalf("open count did not drop: %d", c)
	}

	// Idempotent: a second completion affects 0 rows (no phantom success, no
	// overwrite of the original actor/time).
	if n2, _ := s.q.CompletePendingErasure(ctx, generated.CompletePendingErasureParams{
		ID: rowID, OrgID: org, CompletedBy: pgText("someone-else"),
	}); n2 != 0 {
		t.Fatalf("re-completing an already-done obligation affected %d rows, want 0", n2)
	}
}

// TestCompletePendingErasureIsOrgScoped proves an admin cannot clear another
// tenant's obligation by id: the WHERE also pins org_id, so a cross-org attempt
// affects 0 rows and the victim's obligation stays open.
func TestCompletePendingErasureIsOrgScoped(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()

	victim, attacker := id.New(), id.New()
	for _, o := range []string{victim, attacker} {
		if _, err := pool.Exec(ctx, "INSERT INTO orgs (id, name, slug) VALUES ($1, $2, $3)", o, "O", "o-"+o); err != nil {
			t.Fatalf("seed org: %v", err)
		}
	}
	s := serverForDB(pool)
	s.recordPartialErasure(ctx, victim, "", "org_id", victim, "victim-user", "s3")

	open, _ := s.q.ListOpenErasuresByOrg(ctx, victim)
	if len(open) != 1 {
		t.Fatalf("setup: want 1 obligation for victim, got %d", len(open))
	}
	victimRowID := open[0].ID

	// Attacker's org tries to complete the victim's row by id.
	n, err := s.q.CompletePendingErasure(ctx, generated.CompletePendingErasureParams{
		ID: victimRowID, OrgID: attacker, CompletedBy: pgText("attacker"),
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if n != 0 {
		t.Fatalf("cross-org completion affected %d rows: an admin cleared another tenant's obligation", n)
	}
	if got, _ := s.q.ListOpenErasuresByOrg(ctx, victim); len(got) != 1 {
		t.Fatal("victim's obligation was cleared by another org")
	}
}

// TestCompleteErasureHandlerNotFound proves the handler 404s (not a phantom 204)
// when the id is unknown in the caller's org, so a wrong id is never reported as
// a fulfilled obligation.
func TestCompleteErasureHandlerNotFound(t *testing.T) {
	pool := newTestDB(t)
	s := serverForDB(pool)
	org := id.New()

	req := httptest.NewRequest(http.MethodPost, "/erasures/ghost/complete", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "ghost")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, ctxOrgID, org)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	s.handleCompleteErasure(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown erasure id must 404, got %d (%s)", rec.Code, rec.Body.String())
	}
}

// TestErasureRoutesAreAdminGated is the D4(c) proof at two levels.
//
// (1) The requireRole("admin") gate refuses viewer and member and admits admin+.
// (2) The erasure routes actually sit behind an admin gate in router.go and are
// NOT in the member group (a source assertion, because "which middleware group a
// route sits in" is invisible to a handler-level test and is exactly what drifts
// when a route is added next to a similar one).
func TestErasureRoutesAreAdminGated(t *testing.T) {
	s := &Server{}
	handler := s.requireRole("admin")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, tc := range []struct {
		role string
		want int
	}{
		{"viewer", http.StatusForbidden},
		{"member", http.StatusForbidden},
		{"admin", http.StatusOK},
		{"owner", http.StatusOK},
	} {
		req := httptest.NewRequest(http.MethodPost, "/erasures/x/complete", nil)
		req = req.WithContext(context.WithValue(req.Context(), ctxRole, tc.role))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("role %q: gate returned %d, want %d", tc.role, rec.Code, tc.want)
		}
	}

	// Source assertion: the erasure handlers must be admin-gated, not in the
	// member group.
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("read router.go: %v", err)
	}
	body := string(src)

	memberStart := strings.Index(body, `s.requireRole("member")`)
	if memberStart < 0 {
		t.Fatal(`no requireRole("member") group found in router.go`)
	}
	firstAdmin := strings.Index(body, `s.requireRole("admin")`)
	if firstAdmin < 0 {
		t.Fatal(`no requireRole("admin") group found in router.go`)
	}
	// The member group runs from its requireRole to the first admin/owner gate.
	memberBlock := body[memberStart:firstAdmin]

	for _, h := range []string{"handleListOpenErasures", "handleCompleteErasure"} {
		if strings.Contains(memberBlock, h) {
			t.Errorf("%s sits in the member group: any org API key or member could clear an erasure obligation. Move it behind requireRole(\"admin\").", h)
		}
		if idx := strings.Index(body, h); idx < firstAdmin {
			t.Errorf("%s is wired before the first admin gate in router.go, so it is not admin-gated", h)
		}
	}
}
