package api

import (
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
)

// Routes builds the full HTTP handler: middleware stack, JSON API, and the
// embedded SPA fallback.
func (s *Server) Routes(build fs.FS, csrfMW func(http.Handler) http.Handler) http.Handler {
	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Recoverer)
	r.Use(securityHeaders)
	r.Use(s.sessions.LoadAndSave)

	ccsrf := conditionalCSRF(csrfMW)

	r.Route("/api", func(r chi.Router) {
		r.Get("/health", s.handleHealth)
		r.With(csrfMW).Get("/csrf", s.handleCSRFToken)

		// Ingest: authenticated by the DSN public key, not the session, so
		// these sit outside CSRF and requireAuth. Sentry-wire compatible.
		r.Post("/{projectID}/envelope/", s.handleEnvelope)
		r.Post("/{projectID}/envelope", s.handleEnvelope)
		r.Post("/{projectID}/store/", s.handleStore)
		r.Post("/{projectID}/store", s.handleStore)
		r.Post("/{projectID}/events", s.handleStore)
		r.Post("/{projectID}/logs", s.handleNativeLogs)

		r.Group(func(r chi.Router) {
			r.Use(ccsrf)

			r.Post("/auth/register", s.handleRegister)
			r.Post("/auth/login", s.handleLogin)
			r.Post("/auth/logout", s.handleLogout)
			r.Get("/auth/me", s.handleMe)

			r.Group(func(r chi.Router) {
				r.Use(s.requireAuth)

				r.Post("/projects", s.handleCreateProject)
				r.Get("/projects", s.handleListProjects)
				r.Get("/projects/{id}", s.handleGetProject)
				r.Get("/projects/{id}/issues", s.handleListIssues)
				r.Get("/projects/{id}/logs", s.handleSearchLogs)
				r.Get("/projects/{id}/traces", s.handleListTraces)
				r.Get("/traces/{id}", s.handleGetTrace)
				r.Get("/projects/{id}/alert-rules", s.handleListAlertRules)
				r.Post("/projects/{id}/alert-rules", s.handleCreateAlertRule)

				r.Get("/issues/{id}", s.handleGetIssue)
				r.Get("/issues/{id}/events", s.handleListIssueEvents)
				r.Patch("/issues/{id}", s.handleUpdateIssueStatus)

				r.Get("/channels", s.handleListChannels)
				r.Post("/channels", s.handleCreateChannel)
			})
		})
	})

	// OTLP/HTTP ingest lives at the spec path (clients append /v1/logs to the
	// configured OTLP endpoint). DSN-key auth, no CSRF/session.
	r.Post("/otlp/v1/logs", s.handleOTLPLogs)
	r.Post("/otlp/v1/traces", s.handleOTLPTraces)

	r.Handle("/*", spaHandler(build))
	return r
}
