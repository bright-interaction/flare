package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bright-interaction/flare/internal/auth"
	"github.com/bright-interaction/flare/internal/db/generated"
)

// M4. The CSRF exemption was "an Authorization header is present", which is a
// different question from "is this request authenticated by an ambient
// credential". requireAuth reads the SESSION first and falls through to the API
// key only when there is none, so a junk Bearer header plus a valid session
// cookie was CSRF-exempt AND session-authenticated. The value in the header was
// never looked at.
func TestCSRFExemptionFollowsTheAmbientCredential(t *testing.T) {
	reached := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })
	// A stand-in for gorilla/csrf: refuses everything it is given, so "did the
	// request reach the handler" answers "was it exempt".
	blocking := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})
	}
	h := conditionalCSRF(blocking)(next)

	tests := []struct {
		name        string
		bearer      string
		sessionCook string
		wantExempt  bool
	}{
		{"no credentials at all", "", "", false},
		{"pure API client", "Bearer flare_abc123", "", true},
		{"browser session", "", "sess-value", false},
		{"junk bearer plus a real session", "Bearer x", "sess-value", false},
		{"real bearer plus a real session", "Bearer flare_abc123", "sess-value", false},
		{"whitespace-only bearer", "bEaReR   ", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reached = false
			r := httptest.NewRequest(http.MethodPost, "/api/projects", nil)
			if tt.bearer != "" {
				r.Header.Set("Authorization", tt.bearer)
			}
			if tt.sessionCook != "" {
				r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: tt.sessionCook})
			}
			h.ServeHTTP(httptest.NewRecorder(), r)
			if reached != tt.wantExempt {
				t.Fatalf("exempt=%v, want %v", reached, tt.wantExempt)
			}
		})
	}
}

// M2. The DSN public key is a WRITE credential: the ingest surface sits
// entirely outside requireAuth and authenticates on that key alone. Handing it
// to every reader made "read-only" false, and viewer is the DEFAULT role for a
// minted API key.
func TestViewerDoesNotReceiveAWriteCredential(t *testing.T) {
	s := &Server{}
	p := &generated.Project{ID: "p1", Name: "api", Slug: "api", PublicKey: "pub-key-secret", DsnID: "123456789012"}

	viewer := context.WithValue(context.Background(), ctxRole, "viewer")
	if got := s.toProjectResponse(viewer, p); got.DSN != "" {
		t.Fatalf("viewer received the project DSN: %q", got.DSN)
	}
	member := context.WithValue(context.Background(), ctxRole, "member")
	if got := s.toProjectResponse(member, p); got.DSN == "" {
		t.Fatal("member did not receive the DSN, which is how an SDK gets wired up")
	}

	m := &generated.Monitor{ID: "m1", Slug: "nightly"}
	if got := s.toMonitorResponse(viewer, m, p); got.CheckinURL != "" {
		t.Fatalf("viewer received the check-in URL, which embeds the ingest key: %q", got.CheckinURL)
	}
	if got := s.toMonitorResponse(member, m, p); got.CheckinURL == "" {
		t.Fatal("member did not receive the check-in URL")
	}
}
