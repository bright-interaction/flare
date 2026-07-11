package api

import (
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/bright-interaction/flare/internal/flarereport"
)

// Routes builds the full HTTP handler: middleware stack, JSON API, and the
// embedded SPA fallback.
func (s *Server) Routes(build fs.FS, csrfMW func(http.Handler) http.Handler) http.Handler {
	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	// Trusted-proxy-aware client IP: only honor X-Forwarded-For from an internal
	// proxy peer, so a public client cannot spoof it to dodge the login lockout
	// or ingest rate limit. Replaces chimw.RealIP (unconditional header trust).
	r.Use(realIP)
	r.Use(chimw.Recoverer)
	// Capture panics to Flare (self-reporting), then re-panic for Recoverer's
	// 500. Must sit after Recoverer (inner) so it sees the panic first.
	r.Use(flarereport.FlareRecoverer)
	r.Use(securityHeaders(cspForBuild(build)))
	r.Use(s.sessions.LoadAndSave)

	ccsrf := conditionalCSRF(csrfMW)

	r.Route("/api", func(r chi.Router) {
		r.Get("/health", s.handleHealth)
		r.With(csrfMW).Get("/csrf", s.handleCSRFToken)

		// Ingest: authenticated by the DSN public key, not the session, so
		// these sit outside CSRF and requireAuth. Sentry-wire compatible.
		// Rate-limited per DSN key so a captured key cannot flood writes.
		r.Group(func(r chi.Router) {
			r.Use(s.rateLimitIngest)
			r.Post("/{projectID}/envelope/", s.handleEnvelope)
			r.Post("/{projectID}/envelope", s.handleEnvelope)
			r.Post("/{projectID}/store/", s.handleStore)
			r.Post("/{projectID}/store", s.handleStore)
			r.Post("/{projectID}/events", s.handleStore)
			r.Post("/{projectID}/logs", s.handleNativeLogs)
			r.Post("/{projectID}/metrics", s.handleNativeMetrics)
			// Cron/job check-in: DSN-authed like ingest. GET too, so a bare
			// `curl` from a cron works; ?status=error marks the run failed.
			r.Post("/{projectID}/checkins/{slug}", s.handleCheckin)
			r.Get("/{projectID}/checkins/{slug}", s.handleCheckin)
		})

		r.Group(func(r chi.Router) {
			r.Use(ccsrf)

			r.Post("/auth/register", s.handleRegister)
			r.Post("/auth/login", s.handleLogin)
			r.Post("/auth/logout", s.handleLogout)
			r.Post("/auth/forgot-password", s.handleForgotPassword)
			r.Post("/auth/reset-password", s.handleResetPassword)
			r.Post("/auth/accept-invite", s.handleAcceptInvite)
			r.Get("/auth/me", s.handleMe)
			// SSO browser redirects (GET, so CSRF middleware passes them through).
			r.Get("/auth/oidc/start", s.handleOIDCStart)
			r.Get("/auth/oidc/callback", s.handleOIDCCallback)

			r.Group(func(r chi.Router) {
				r.Use(s.requireAuth)

				// MCP: one JSON-RPC endpoint for an AI operator (Claude). Same
				// auth as the REST API (session or org API key); write tools
				// self-gate to member+ inside the handler; per-org rate limited.
				// POST-only.
				r.With(s.rateLimitMCP).Handle("/mcp", s.mcpHandler())

				// Reads: any authenticated role (viewer+).
				r.Get("/overview", s.handleOverview)
				r.Get("/projects", s.handleListProjects)
				r.Get("/projects/{id}", s.handleGetProject)
				r.Get("/projects/{id}/issues", s.handleListIssues)
				r.Get("/projects/{id}/logs", s.handleSearchLogs)
				r.Get("/projects/{id}/traces", s.handleListTraces)
				r.Get("/projects/{id}/traces/{traceID}", s.handleGetTrace)
				r.Get("/projects/{id}/analytics/log-volume", s.handleLogVolume)
				r.Get("/projects/{id}/analytics/span-latency", s.handleSpanLatency)
				r.Get("/projects/{id}/metrics", s.handleListMetrics)
				r.Get("/projects/{id}/metrics/query", s.handleQueryMetric)
				r.Get("/projects/{id}/alert-rules", s.handleListAlertRules)
				r.Get("/projects/{id}/monitors", s.handleListMonitors)
				r.Get("/projects/{id}/artifacts", s.handleListSourceMaps)
				r.Get("/projects/{id}/releases", s.handleListReleases)
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
					r.Post("/projects/{id}/monitors", s.handleCreateMonitor)
					r.Patch("/monitors/{id}", s.handleUpdateMonitor)
					r.Delete("/monitors/{id}", s.handleDeleteMonitor)
					r.Post("/projects/{id}/artifacts", s.handleUploadSourceMap)
					r.Delete("/projects/{id}/artifacts/{artifactID}", s.handleDeleteSourceMap)
					r.Post("/projects/{id}/releases", s.handleCreateRelease)
					r.Patch("/issues/{id}", s.handleUpdateIssueStatus)
					r.Post("/issues/{id}/triage", s.handleTriageIssue)
					r.Post("/channels", s.handleCreateChannel)
					r.Post("/channels/{id}/test", s.handleTestChannel)
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
					r.Get("/audit-log", s.handleListAuditLog)
					r.Get("/export", s.handleExport)
					r.Get("/integrations/oidc", s.handleGetOIDCConfig)
					r.Put("/integrations/oidc", s.handleSetOIDCConfig)
					r.Delete("/integrations/oidc", s.handleDeleteOIDCConfig)
					r.Get("/integrations/ai", s.handleGetAIConfig)
					r.Put("/integrations/ai", s.handleSetAIConfig)
					r.Delete("/integrations/ai", s.handleDeleteAIConfig)
				})

				// Workspace erasure: owner only.
				r.With(s.requireRole("owner")).Delete("/org", s.handleDeleteOrg)
			})
		})
	})

	// OTLP/HTTP ingest lives at the spec path (clients append /v1/logs to the
	// configured OTLP endpoint). DSN-key auth, no CSRF/session, same rate limit.
	r.With(s.rateLimitIngest).Post("/otlp/v1/logs", s.handleOTLPLogs)
	r.With(s.rateLimitIngest).Post("/otlp/v1/traces", s.handleOTLPTraces)
	r.With(s.rateLimitIngest).Post("/otlp/v1/metrics", s.handleOTLPMetrics)

	// Deep-link from a DSN (numeric dsn_id) to the dashboard project page.
	r.Get("/go/{dsnID}", s.handleDSNRedirect)

	r.Handle("/*", spaHandler(build))
	return r
}
