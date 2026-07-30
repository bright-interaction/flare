package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bright-interaction/flare/internal/db"
	"github.com/bright-interaction/flare/internal/db/generated"
	"github.com/bright-interaction/flare/internal/id"
)

// These tests drive the REAL handleUpdateChannel / handleListChannels against a
// real postgres:16, one throwaway database per test. They are skipped unless
// FLARE_TEST_DATABASE_URL points at a superuser connection (CREATE DATABASE),
// so the default `go test ./...` stays green with no DB. Run them with, e.g.:
//
//	FLARE_TEST_DATABASE_URL='postgres://test:test@127.0.0.1:55439/flare?sslmode=disable' \
//	  go test ./internal/api/ -run UpdateChannel -count=1 -v
//
// The finding they pin: the update-channel routing write was non-transactional,
// so the 'enabled' toggle autocommitted before project validation (F2), and a
// mid-loop insert failure could leave routing empty, which reads as "all
// projects" (D1/F1). The list read swallowed errors and rendered empty=all
// (F3). project_ids was uncapped and un-deduplicated (F4).

// newTestDB creates a uniquely-named database on the admin connection, migrates
// it, and returns a pool to it plus a cleanup that drops it.
func newTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	adminURL := os.Getenv("FLARE_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("FLARE_TEST_DATABASE_URL not set; skipping DB-backed update-channel tests")
	}
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()

	dbName := "flare_test_" + strings.ToLower(id.New())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+dbName); err != nil {
		t.Fatalf("create database: %v", err)
	}

	// Swap the database name in the URL for the fresh one.
	testURL := swapDBName(adminURL, dbName)
	if err := db.RunMigrations(ctx, testURL); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(ctx, testURL)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		a, err := pgxpool.New(ctx, adminURL)
		if err != nil {
			return
		}
		defer a.Close()
		_, _ = a.Exec(ctx, "DROP DATABASE IF EXISTS "+dbName+" WITH (FORCE)")
	})
	return pool
}

// swapDBName replaces the path component (database name) of a postgres URL.
func swapDBName(url, name string) string {
	// url looks like scheme://user:pass@host:port/dbname?query
	q := ""
	if i := strings.IndexByte(url, '?'); i >= 0 {
		q = url[i:]
		url = url[:i]
	}
	slash := strings.LastIndexByte(url, '/')
	return url[:slash+1] + name + q
}

// seedOrgProjectChannel inserts one org, one channel (enabled), and n projects,
// returning the org id, channel id, and the project ids.
func seedOrgProjectChannel(t *testing.T, pool *pgxpool.Pool, nProjects int) (org, chID string, projIDs []string) {
	t.Helper()
	ctx := context.Background()
	q := generated.New(pool)

	org = id.New()
	if _, err := pool.Exec(ctx, "INSERT INTO orgs (id, name, slug) VALUES ($1, $2, $3)", org, "Acme", "acme-"+org); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	for i := 0; i < nProjects; i++ {
		p, err := q.CreateProject(ctx, generated.CreateProjectParams{
			ID: id.New(), OrgID: org, Name: fmt.Sprintf("proj-%d", i),
			Slug: fmt.Sprintf("proj-%d-%s", i, id.New()), Platform: "other",
			PublicKey: id.New(), DsnID: id.New(),
		})
		if err != nil {
			t.Fatalf("seed project: %v", err)
		}
		projIDs = append(projIDs, p.ID)
	}
	ch, err := q.CreateNotificationChannel(ctx, generated.CreateNotificationChannelParams{
		ID: id.New(), OrgID: org, Type: "log", Config: json.RawMessage(`{}`), Enabled: true,
	})
	if err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	chID = ch.ID
	return org, chID, projIDs
}

// serverForDB builds the minimal Server the update/list handlers touch.
func serverForDB(pool *pgxpool.Pool) *Server {
	return &Server{q: generated.New(pool), pool: pool}
}

// patchChannel drives the real handler with an org in context and returns the
// recorder.
func patchChannel(s *Server, org, chID string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPatch, "/channels/"+chID, strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", chID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, ctxOrgID, org)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	s.handleUpdateChannel(rec, req)
	return rec
}

func routingRows(t *testing.T, pool *pgxpool.Pool, chID string) []string {
	t.Helper()
	pids, err := generated.New(pool).ListChannelProjects(context.Background(), chID)
	if err != nil {
		t.Fatalf("read routing: %v", err)
	}
	return pids
}

func channelEnabled(t *testing.T, pool *pgxpool.Pool, org, chID string) bool {
	t.Helper()
	ch, err := generated.New(pool).GetNotificationChannel(context.Background(),
		generated.GetNotificationChannelParams{ID: chID, OrgID: org})
	if err != nil {
		t.Fatalf("get channel: %v", err)
	}
	return ch.Enabled
}

// setRouting narrows the channel to exactly the given projects, so a test starts
// from a known non-empty routing (not the empty=all default).
func setRouting(t *testing.T, pool *pgxpool.Pool, chID string, projIDs ...string) {
	t.Helper()
	ctx := context.Background()
	q := generated.New(pool)
	if err := q.ReplaceChannelProjects(ctx, chID); err != nil {
		t.Fatalf("clear routing: %v", err)
	}
	for _, p := range projIDs {
		if err := q.AddChannelProject(ctx, generated.AddChannelProjectParams{ChannelID: chID, ProjectID: p}); err != nil {
			t.Fatalf("seed routing: %v", err)
		}
	}
}

// TestUpdateChannelValidationRollsBackEnabled is the atomicity proof (F2 + F1).
// A PATCH that flips enabled AND supplies an invalid project id must persist
// NOTHING: the enabled write and the routing replace are in one transaction, so
// the 400 rolls both back. On the pre-fix code the enabled write autocommitted
// before validation, so this asserts RED there.
func TestUpdateChannelValidationRollsBackEnabled(t *testing.T) {
	pool := newTestDB(t)
	org, chID, projs := seedOrgProjectChannel(t, pool, 1)
	setRouting(t, pool, chID, projs[0]) // start narrowed to [p0], enabled=true

	body := fmt.Sprintf(`{"enabled":false,"project_ids":[%q,%q]}`, projs[0], "ghost-does-not-exist")
	rec := patchChannel(serverForDB(pool), org, chID, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 on unknown project, got %d (%s)", rec.Code, rec.Body.String())
	}
	// enabled must be UNCHANGED (still true): the write was rolled back.
	if !channelEnabled(t, pool, org, chID) {
		t.Fatal("enabled was persisted despite the PATCH failing: the enabled write did not roll back (F2)")
	}
	// routing must be UNCHANGED (still [p0]), never emptied.
	got := routingRows(t, pool, chID)
	if len(got) != 1 || got[0] != projs[0] {
		t.Fatalf("routing changed on a failed PATCH: want [%s], got %v (F1: empty here would read as all-projects)", projs[0], got)
	}
}

// TestUpdateChannelPartialInsertLeavesOriginalRouting reproduces the D1/F1
// mid-loop-insert failure via a concurrently-deleted project. It commits a
// delete of one routed project id inside the same request context window using
// the READ COMMITTED default: with the fix, the whole PATCH runs in one tx, so
// the FK failure (or the validation seeing the delete) rolls back and the
// ORIGINAL routing survives, never the empty=all state.
func TestUpdateChannelPartialInsertLeavesOriginalRouting(t *testing.T) {
	pool := newTestDB(t)
	org, chID, projs := seedOrgProjectChannel(t, pool, 2)
	setRouting(t, pool, chID, projs[0]) // original routing: [p0]

	// Delete p1 (committed) so that a PATCH trying to route to [p1] must fail
	// its insert/validation. With the transactional fix the failure rolls the
	// whole PATCH back and the original [p0] routing is preserved.
	if _, err := pool.Exec(context.Background(), "DELETE FROM projects WHERE id = $1", projs[1]); err != nil {
		t.Fatalf("delete project: %v", err)
	}

	body := fmt.Sprintf(`{"project_ids":[%q]}`, projs[1])
	rec := patchChannel(serverForDB(pool), org, chID, body)
	if rec.Code == http.StatusOK {
		t.Fatalf("PATCH to a deleted project should not succeed, got 200")
	}
	got := routingRows(t, pool, chID)
	if len(got) != 1 || got[0] != projs[0] {
		t.Fatalf("original routing lost after a failed PATCH: want [%s], got %v (empty=all broadcast)", projs[0], got)
	}
}

// TestUpdateChannelOverCapRejected pins F4's cap. 300 distinct ids must be
// rejected with the cap error BEFORE any per-project validation, so the fake
// ids never even reach GetProjectByID. On the pre-fix code (no cap) this hit
// the validation loop and returned "unknown project" instead, so the message
// assertion is RED there.
func TestUpdateChannelOverCapRejected(t *testing.T) {
	pool := newTestDB(t)
	org, chID, _ := seedOrgProjectChannel(t, pool, 1)

	ids := make([]string, 0, 300)
	for i := 0; i < 300; i++ {
		ids = append(ids, id.New())
	}
	raw, _ := json.Marshal(map[string]any{"project_ids": ids})
	rec := patchChannel(serverForDB(pool), org, chID, string(raw))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for over-cap, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "too many project_ids") {
		t.Fatalf("want the cap error (over-cap must be rejected before validation), got %s", rec.Body.String())
	}
}

// TestUpdateChannelDedup pins F4's dedup: the same valid id repeated far past
// the cap must SUCCEED (collapsed to one) and route to exactly that project.
// This guards the invariant that dedup runs before the cap check, so a future
// cap-without-dedup regression goes red here.
func TestUpdateChannelDedup(t *testing.T) {
	pool := newTestDB(t)
	org, chID, projs := seedOrgProjectChannel(t, pool, 1)

	ids := make([]string, 0, 300)
	for i := 0; i < 300; i++ {
		ids = append(ids, projs[0])
	}
	raw, _ := json.Marshal(map[string]any{"project_ids": ids})
	rec := patchChannel(serverForDB(pool), org, chID, string(raw))

	if rec.Code != http.StatusOK {
		t.Fatalf("duplicate-heavy list should dedup and succeed, got %d (%s)", rec.Code, rec.Body.String())
	}
	got := routingRows(t, pool, chID)
	if len(got) != 1 || got[0] != projs[0] {
		t.Fatalf("want routing [%s], got %v", projs[0], got)
	}
}

// TestUpdateChannelLegitNarrowSucceeds proves the fix does not break the happy
// path: a valid narrow PATCH scopes the channel and preserves enabled.
func TestUpdateChannelLegitNarrowSucceeds(t *testing.T) {
	pool := newTestDB(t)
	org, chID, projs := seedOrgProjectChannel(t, pool, 3)

	body := fmt.Sprintf(`{"project_ids":[%q,%q]}`, projs[0], projs[2])
	rec := patchChannel(serverForDB(pool), org, chID, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("legit PATCH should succeed, got %d (%s)", rec.Code, rec.Body.String())
	}
	got := routingRows(t, pool, chID)
	if len(got) != 2 {
		t.Fatalf("want 2 routed projects, got %v", got)
	}
	set := map[string]bool{got[0]: true, got[1]: true}
	if !set[projs[0]] || !set[projs[2]] {
		t.Fatalf("routing scoped wrong: want {%s,%s}, got %v", projs[0], projs[2], got)
	}
	if !channelEnabled(t, pool, org, chID) {
		t.Fatal("enabled flipped unexpectedly on a routing-only PATCH")
	}
}

// TestListChannelsFailsClosedOnRoutingError pins F3: if the routing read fails,
// the list must NOT degrade to empty project_ids (which renders as all
// projects). It drops channel_projects to force the error and asserts a 500.
// On the pre-fix code the error was swallowed and the response was 200 with an
// empty routing list, so this asserts RED there.
func TestListChannelsFailsClosedOnRoutingError(t *testing.T) {
	pool := newTestDB(t)
	org, _, _ := seedOrgProjectChannel(t, pool, 1)

	if _, err := pool.Exec(context.Background(), "DROP TABLE channel_projects"); err != nil {
		t.Fatalf("drop channel_projects: %v", err)
	}

	s := serverForDB(pool)
	req := httptest.NewRequest(http.MethodGet, "/channels", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxOrgID, org))
	rec := httptest.NewRecorder()
	s.handleListChannels(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("routing read error must fail closed (500), got %d (%s): a channel that is actually narrowed would render as all-projects", rec.Code, rec.Body.String())
	}
}
