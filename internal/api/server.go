// Package api wires Flare's HTTP surface: auth, projects, ingest, and the
// embedded SvelteKit dashboard.
package api

import (
	"fmt"
	"strings"

	"github.com/alexedwards/scs/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bright-interaction/flare/internal/alerts"
	"github.com/bright-interaction/flare/internal/analytics"
	"github.com/bright-interaction/flare/internal/config"
	"github.com/bright-interaction/flare/internal/db/generated"
	"github.com/bright-interaction/flare/internal/email"
	"github.com/bright-interaction/flare/internal/telemetry"
	"github.com/bright-interaction/flare/internal/telemetry/pgstore"
)

type Server struct {
	q          *generated.Queries
	pool       *pgxpool.Pool
	store      telemetry.Store
	analytics  *analytics.Manager // may be nil when DuckDB failed to open
	sessions   *scs.SessionManager
	cfg        config.Config
	dispatcher *alerts.Dispatcher
	mailer     *email.Mailer
}

func NewServer(pool *pgxpool.Pool, sessions *scs.SessionManager, cfg config.Config, analyticsMgr *analytics.Manager) *Server {
	mailer := email.New(cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPUser, cfg.SMTPPass, cfg.SMTPFrom, cfg.SMTPFromName, cfg.SMTPTLS)
	return &Server{
		q:          generated.New(pool),
		pool:       pool,
		store:      pgstore.New(pool),
		analytics:  analyticsMgr,
		sessions:   sessions,
		cfg:        cfg,
		dispatcher: alerts.NewDispatcher(mailer),
		mailer:     mailer,
	}
}

// dsn builds the Sentry-style DSN a client SDK uses to point at this project.
// Shape: {scheme}://{publicKey}@{host}/{dsnID}. dsnID is numeric because
// @sentry/* SDKs reject a non-numeric project id in the DSN path.
func (s *Server) dsn(publicKey, dsnID string) string {
	scheme, host := splitBaseURL(s.cfg.BaseURL)
	return fmt.Sprintf("%s://%s@%s/%s", scheme, publicKey, host, dsnID)
}

// otlpEndpoint is the OTLP/HTTP base a collector or SDK exports logs+traces to.
func (s *Server) otlpEndpoint() string {
	return strings.TrimRight(s.cfg.BaseURL, "/") + "/otlp"
}

func splitBaseURL(base string) (scheme, host string) {
	scheme = "https"
	host = base
	if i := strings.Index(base, "://"); i >= 0 {
		scheme = base[:i]
		host = base[i+3:]
	}
	host = strings.TrimRight(host, "/")
	return scheme, host
}
