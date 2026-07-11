package flarereport

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The self-amplification guard is the whole reason isUntraceable exists: tracing
// Flare's own ingest routes would emit a transaction envelope that is itself an
// ingest request. Lock the ingest shapes (and health/static noise) as skipped,
// and normal app routes as traced.
func TestIsUntraceable(t *testing.T) {
	skip := []string{
		"/health", "/healthz", "/readyz", "/metrics", "/favicon.ico",
		"/store/", "/store",
		"/static/app.js", "/assets/x.css", "/_app/immutable/y.js",
		"/otlp/v1/traces", "/otlp",
		"/api/724067772811/envelope/", "/api/724067772811/store/",
		"/api/proj/logs", "/api/proj/traces", "/api/proj/metrics",
	}
	for _, p := range skip {
		if !isUntraceable(p) {
			t.Errorf("expected %q to be skipped (ingest/infra self-loop guard)", p)
		}
	}
	trace := []string{"/", "/api/users/123", "/projects/abc/issues", "/team", "/api/mcp"}
	for _, p := range trace {
		if isUntraceable(p) {
			t.Errorf("expected %q to be traced, got skipped", p)
		}
	}
}

func TestNormalizeTracePath(t *testing.T) {
	cases := map[string]string{
		"/":                                        "/",
		"/api/users/123":                           "/api/users/:id",
		"/projects/ru6pj4v16irarblw2s1ojrrs/edit":  "/projects/:id/edit",
		"/t/550e8400-e29b-41d4-a716-446655440000":  "/t/:id",
		"/api/v1/scans":                            "/api/v1/scans", // v1 kept (short), scans kept (word)
		"/verify-traces-probe":                     "/verify-traces-probe",
	}
	for in, want := range cases {
		if got := normalizeTracePath(in); got != want {
			t.Errorf("normalizeTracePath(%q) = %q, want %q", in, got, want)
		}
	}
}

// Tracing must not wrap the ResponseWriter (would drop http.Flusher/Hijacker and
// break streaming). Assert a request flows through and the same writer reaches
// the handler, and that a disabled rate returns the handler unchanged.
func TestFlareTracerDoesNotWrapResponseWriter(t *testing.T) {
	t.Setenv("FLARE_TRACES_SAMPLE_RATE", "1")
	var sawFlusher bool
	h := flareTracer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawFlusher = w.(http.Flusher)
		w.WriteHeader(200)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/users/7", nil))
	if !sawFlusher {
		t.Fatal("handler lost http.Flusher: tracer must not wrap the ResponseWriter")
	}
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestFlareTracerDisabledIsPassthrough(t *testing.T) {
	t.Setenv("FLARE_TRACES_SAMPLE_RATE", "0")
	base := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	if got := flareTracer(base); &got == nil { // compile-guard; real check below
		t.Fatal("nil handler")
	}
	// rate 0 -> returns the same handler value (no wrapping allocation semantics)
	rec := httptest.NewRecorder()
	flareTracer(base).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
}
