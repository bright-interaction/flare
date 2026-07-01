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
		// Set HSTS in-app so a self-host behind any proxy is protected, not only
		// deployments that happen to add it at the edge. Browsers ignore HSTS
		// received over plain HTTP, so this is a no-op in local dev.
		h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=(), interest-cohort=()")
		next.ServeHTTP(w, r)
	})
}

// rateLimitIngest caps event ingest per DSN public key (falling back to client
// IP when no key is present) so a captured DSN cannot flood the write path.
func (s *Server) rateLimitIngest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := ingestKey(r)
		bucket := "k:" + key
		if key == "" {
			bucket = "ip:" + clientIP(r)
		}
		if !s.ingestLimiter.Allow(bucket) {
			w.Header().Set("Retry-After", "60")
			writeErr(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP returns the caller IP without the port. chi's RealIP middleware has
// already resolved X-Forwarded-For into RemoteAddr upstream.
func clientIP(r *http.Request) string {
	addr := r.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i > 0 {
		return addr[:i]
	}
	return addr
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
