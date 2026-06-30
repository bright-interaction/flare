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
			r.Post("/auth/forgot-password", s.handleForgotPassword)
			r.Post("/auth/reset-password", s.handleResetPassword)
			r.Post("/auth/accept-invite", s.handleAcceptInvite)
			r.Get("/auth/me", s.handleMe)

			r.Group(func(r chi.Router) {
				r.Use(s.requireAuth)

				// Reads: any authenticated role (viewer+).
				r.Get("/projects", s.handleListProjects)
				r.Get("/projects/{id}", s.handleGetProject)
				r.Get("/projects/{id}/issues", s.handleListIssues)
				r.Get("/projects/{id}/logs", s.handleSearchLogs)
				r.Get("/projects/{id}/traces", s.handleListTraces)
				r.Get("/projects/{id}/traces/{traceID}", s.handleGetTrace)
				r.Get("/projects/{id}/analytics/log-volume", s.handleLogVolume)
				r.Get("/projects/{id}/analytics/span-latency", s.handleSpanLatency)
				r.Get("/projects/{id}/alert-rules", s.handleListAlertRules)
				r.Get("/projects/{id}/artifacts", s.handleListSourceMaps)
				r.Get("/issues/{id}", s.handleGetIssue)
				r.Get("/issues/{id}/events", s.handleListIssueEvents)
				r.Get("/channels", s.handleListChannels)
				r.Get("/keys", s.handleListAPIKeys)
				r.Get("/members", s.handleListMembers)
				r.Get("/integrations/github", s.handleGetGithubConfig)

				// Writes: member+ (viewers are read-only). API keys act here.
				r.Group(func(r chi.Router) {
					r.Use(s.requireRole("member"))

					r.Post("/projects", s.handleCreateProject)
					r.Post("/projects/provision", s.handleProvisionProject)
					r.Delete("/projects/{id}", s.handleDeleteProject)
					r.Post("/projects/{id}/alert-rules", s.handleCreateAlertRule)
					r.Delete("/projects/{id}/alert-rules/{ruleID}", s.handleDeleteAlertRule)
					r.Post("/projects/{id}/artifacts", s.handleUploadSourceMap)
					r.Delete("/projects/{id}/artifacts/{artifactID}", s.handleDeleteSourceMap)
					r.Patch("/issues/{id}", s.handleUpdateIssueStatus)
					r.Post("/channels", s.handleCreateChannel)
					r.Delete("/channels/{id}", s.handleDeleteChannel)
					r.Post("/keys", s.handleCreateAPIKey)
					r.Delete("/keys/{id}", s.handleDeleteAPIKey)
					r.Post("/issues/{id}/github", s.handleCreateGithubIssue)
				})

				// Team management + integration secrets: admin+.
				r.Group(func(r chi.Router) {
					r.Use(s.requireRole("admin"))

					r.Patch("/members/{userID}", s.handleUpdateMemberRole)
					r.Delete("/members/{userID}", s.handleRemoveMember)
					r.Get("/invites", s.handleListInvites)
					r.Post("/invites", s.handleInviteMember)
					r.Delete("/invites/{inviteID}", s.handleRevokeInvite)
					r.Put("/integrations/github", s.handleSetGithubConfig)
					r.Delete("/integrations/github", s.handleDeleteGithubConfig)
				})
			})
		})
	})

	// OTLP/HTTP ingest lives at the spec path (clients append /v1/logs to the
	// configured OTLP endpoint). DSN-key auth, no CSRF/session.
	r.Post("/otlp/v1/logs", s.handleOTLPLogs)
	r.Post("/otlp/v1/traces", s.handleOTLPTraces)

	// Deep-link from a DSN (numeric dsn_id) to the dashboard project page.
	r.Get("/go/{dsnID}", s.handleDSNRedirect)

	r.Handle("/*", spaHandler(build))
	return r
}
