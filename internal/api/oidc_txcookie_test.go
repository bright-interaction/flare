package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bright-interaction/flare/internal/config"
)

func txServer() *Server {
	return &Server{cfg: config.Config{SecretKey: "test-secret-key-for-oidc-tx-cookie", Environment: "production"}}
}

// carry moves the cookie the response set onto a fresh request, the way a
// browser would on the IdP's redirect back.
func carry(t *testing.T, rec *httptest.ResponseRecorder) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/callback", nil)
	for _, c := range rec.Result().Cookies() {
		r.AddCookie(c)
	}
	return r
}

func TestOIDCTxRoundTrip(t *testing.T) {
	s := txServer()
	rec := httptest.NewRecorder()
	s.setOIDCTx(rec, "state-abc", "org-123")

	gotState, gotOrg := s.readOIDCTx(carry(t, rec))
	if gotState != "state-abc" || gotOrg != "org-123" {
		t.Fatalf("round trip = (%q, %q), want (state-abc, org-123)", gotState, gotOrg)
	}
}

// The whole reason this cookie exists: the session cookie is SameSite=Strict,
// so the browser withholds it on the IdP's cross-site redirect back and SSO
// could never complete. Lax is sent on exactly that navigation.
func TestOIDCTxCookieAttributes(t *testing.T) {
	s := txServer()
	rec := httptest.NewRecorder()
	s.setOIDCTx(rec, "state-abc", "org-123")

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected exactly one cookie, got %d", len(cookies))
	}
	c := cookies[0]
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax (Strict is withheld on the IdP return)", c.SameSite)
	}
	if !c.HttpOnly {
		t.Error("cookie must be HttpOnly")
	}
	if !c.Secure {
		t.Error("cookie must be Secure in production")
	}
	if c.MaxAge <= 0 || c.MaxAge > 900 {
		t.Errorf("MaxAge = %d, want a short positive TTL", c.MaxAge)
	}
}

func TestOIDCTxRejectsTampering(t *testing.T) {
	s := txServer()
	rec := httptest.NewRecorder()
	s.setOIDCTx(rec, "state-abc", "org-123")
	original := rec.Result().Cookies()[0].Value

	tests := []struct {
		name  string
		value string
	}{
		{"empty", ""},
		{"garbage", "not-a-cookie"},
		{"too few parts", "a.b.c"},
		{"truncated mac", original[:len(original)-4]},
		// Forge a different org with a signature that cannot match.
		{"swapped org", "c3RhdGUtYWJj.b3RoZXItb3Jn.99999999999.AAAA"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/callback", nil)
			r.AddCookie(&http.Cookie{Name: oidcTxCookie, Value: tt.value})
			if st, org := s.readOIDCTx(r); st != "" || org != "" {
				t.Fatalf("tampered cookie accepted: (%q, %q)", st, org)
			}
		})
	}
}

// A cookie signed with a different key must never validate.
func TestOIDCTxRejectsForeignKey(t *testing.T) {
	rec := httptest.NewRecorder()
	txServer().setOIDCTx(rec, "state-abc", "org-123")

	other := &Server{cfg: config.Config{SecretKey: "a-completely-different-secret-key"}}
	if st, org := other.readOIDCTx(carry(t, rec)); st != "" || org != "" {
		t.Fatalf("cookie from another key accepted: (%q, %q)", st, org)
	}
}

func TestOIDCTxClearExpiresTheCookie(t *testing.T) {
	s := txServer()
	rec := httptest.NewRecorder()
	s.clearOIDCTx(rec)
	c := rec.Result().Cookies()[0]
	if c.MaxAge >= 0 {
		t.Fatalf("MaxAge = %d, want negative so the browser drops it", c.MaxAge)
	}
	if c.Value != "" {
		t.Fatalf("Value = %q, want empty", c.Value)
	}
}
