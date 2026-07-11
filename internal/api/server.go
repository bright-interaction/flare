// Package api wires Flare's HTTP surface: auth, projects, ingest, and the
// embedded SvelteKit dashboard.
package api

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bright-interaction/flare/internal/ai"
	"github.com/bright-interaction/flare/internal/alerts"
	"github.com/bright-interaction/flare/internal/analytics"
	"github.com/bright-interaction/flare/internal/config"
	"github.com/bright-interaction/flare/internal/db/generated"
	"github.com/bright-interaction/flare/internal/email"
	"github.com/bright-interaction/flare/internal/ratelimit"
	"github.com/bright-interaction/flare/internal/secretbox"
	"github.com/bright-interaction/flare/internal/sourcemaps"
	"github.com/bright-interaction/flare/internal/telemetry"
	"github.com/bright-interaction/flare/internal/telemetry/pgstore"
)

const (
	// loginFailBudget / loginFailWindow lock an account+IP out after this many
	// failed logins, per the repo security rules (5 fails, 15-min cooldown).
	loginFailBudget = 5
	loginFailWindow = 15 * time.Minute
)

type Server struct {
	q            *generated.Queries
	pool         *pgxpool.Pool
	store        telemetry.Store
	analytics    *analytics.Manager // may be nil when DuckDB failed to open
	sessions     *scs.SessionManager
	cfg          config.Config
	dispatcher   *alerts.Dispatcher
	mailer       *email.Mailer
	symbolicator *sourcemaps.Resolver
	ai           *ai.Client
	secrets      *secretbox.Cipher // encrypts integration secrets at rest

	// loginLimiter locks out brute-force logins; ingestLimiter caps per-DSN-key
	// (or per-IP) ingest flooding; mcpLimiter caps per-org MCP request rate so a
	// member key cannot loop an expensive tool. All in-memory, no Redis.
	loginLimiter  *ratelimit.Limiter
	ingestLimiter *ratelimit.Limiter
	mcpLimiter    *ratelimit.Limiter
	// resetLimiter caps password-reset requests per email+IP so /forgot-password
	// cannot be used for account enumeration or reset-email bombing.
	resetLimiter *ratelimit.Limiter

	// Security-event recording: Flare's own security signals (ingest-auth
	// rejections, login lockouts) become grouped issues in a per-org
	// "flare-security" project. secIPLimiter throttles per (kind, ip),
	// secGlobalLimiter caps total volume per kind, so a flood of rejected
	// requests cannot self-DoS the write path. secMu guards the caches.
	secMu            sync.Mutex
	systemOrg        string
	secProjects      map[string]*generated.Project
	secIPLimiter     *ratelimit.Limiter
	secGlobalLimiter *ratelimit.Limiter
}

func NewServer(pool *pgxpool.Pool, sessions *scs.SessionManager, cfg config.Config, analyticsMgr *analytics.Manager) *Server {
	mailer := email.New(cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPUser, cfg.SMTPPass, cfg.SMTPFrom, cfg.SMTPFromName, cfg.SMTPTLS)
	srv := &Server{
		q:            generated.New(pool),
		pool:         pool,
		store:        pgstore.New(pool),
		analytics:    analyticsMgr,
		sessions:     sessions,
		cfg:          cfg,
		dispatcher:   alerts.NewDispatcher(mailer),
		mailer:       mailer,
		symbolicator: sourcemaps.NewResolver(),
		ai:           ai.New(cfg.IsProduction()),
		secrets:      secretbox.New(cfg.SecretKey),

		loginLimiter:  ratelimit.New(loginFailBudget, loginFailWindow),
		ingestLimiter: ratelimit.New(cfg.IngestRatePerMin, time.Minute),
		mcpLimiter:    ratelimit.New(mcpRatePerMin, time.Minute),
		resetLimiter:  ratelimit.New(5, 15*time.Minute), // <=5 reset requests per (email, ip) / 15m

		secProjects:      map[string]*generated.Project{},
		secIPLimiter:     ratelimit.New(1, 10*time.Second), // <=1 per (kind, ip) / 10s
		secGlobalLimiter: ratelimit.New(60, time.Minute),   // <=60 per kind / min
	}
	// Persist each delivery outcome so a silently-failing channel becomes
	// visible (last_ok_at / last_error) instead of vanishing into a log line.
	srv.dispatcher.Recorder = srv.recordChannelDelivery
	return srv
}

// recordChannelDelivery persists the outcome of one notification delivery
// attempt. Best-effort: a failed write must never affect alert dispatch. Runs
// in the dispatch goroutine (ingest/watchdog) or the test-send request.
func (s *Server) recordChannelDelivery(ctx context.Context, orgID, channelID string, derr error) {
	errMsg := pgtype.Text{}
	ok := derr == nil
	if !ok {
		msg := derr.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
		errMsg = pgtype.Text{String: msg, Valid: true}
	}
	if err := s.q.RecordChannelDelivery(ctx, generated.RecordChannelDeliveryParams{
		AttemptedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		Ok:          ok,
		ErrorMsg:    errMsg,
		ID:          channelID,
		OrgID:       orgID,
	}); err != nil {
		slog.Warn("record channel delivery", "channel_id", channelID, "error", err)
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
