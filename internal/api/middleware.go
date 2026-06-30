package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/bright-interaction/flare/internal/auth"
)

// securityHeaders sets the baseline response headers required by the repo
// security rules.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// requireAuth accepts either a browser session (dashboard) or a Bearer API
// key (programmatic). It injects the user and org ids into the request
// context for downstream handlers.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Session: re-read the user row so a removed user is rejected and a
		// role change takes effect on the next request (no stale session role).
		if uid := s.sessions.GetString(ctx, "user_id"); uid != "" {
			if user, err := s.q.GetUserByID(ctx, uid); err == nil {
				ctx = context.WithValue(ctx, ctxUserID, user.ID)
				ctx = context.WithValue(ctx, ctxOrgID, user.OrgID)
				ctx = context.WithValue(ctx, ctxRole, user.Role)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}

		// API key: org-scoped programmatic access (provisioning, source-map
		// upload, ingest). Acts at member level, never team management.
		if key := bearerToken(r); key != "" {
			ak, err := s.q.GetAPIKeyByHash(ctx, auth.HashAPIKey(key))
			if err == nil && (!ak.ExpiresAt.Valid || ak.ExpiresAt.Time.After(time.Now())) {
				_ = s.q.TouchAPIKey(ctx, ak.ID)
				ctx = context.WithValue(ctx, ctxOrgID, ak.OrgID)
				ctx = context.WithValue(ctx, ctxRole, "member")
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}

		writeErr(w, http.StatusUnauthorized, "authentication required")
	})
}

// requireRole returns middleware that rejects callers below min in the role
// hierarchy with 403.
func (s *Server) requireRole(min string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !roleAtLeast(roleFrom(r.Context()), min) {
				writeErr(w, http.StatusForbidden, "insufficient permissions for this action")
				return
			}
			next.ServeHTTP(w, r.WithContext(r.Context()))
		})
	}
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}
