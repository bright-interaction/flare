package api

import (
	"net/http"

	"github.com/gorilla/csrf"
)

// conditionalCSRF enforces CSRF on cookie-authenticated (browser) requests
// but skips it when a Bearer API key is present. Bearer clients are not
// browsers and carry no ambient cookie, so they are immune to CSRF; the
// security rules explicitly allow them to skip the check.
func conditionalCSRF(csrfMW func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		protected := csrfMW(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if bearerToken(r) != "" {
				next.ServeHTTP(w, r)
				return
			}
			protected.ServeHTTP(w, r)
		})
	}
}

// handleCSRFToken returns the per-session CSRF token the SPA echoes back in
// the X-CSRF-Token header on state-changing requests.
func (s *Server) handleCSRFToken(w http.ResponseWriter, r *http.Request) {
	token := ""
	if !(s.cfg.DisableCSRF && !s.cfg.IsProduction()) {
		token = csrf.Token(r)
	}
	writeJSON(w, http.StatusOK, map[string]string{"csrf_token": token})
}
