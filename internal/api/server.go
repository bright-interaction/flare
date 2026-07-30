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
	// testLimiter caps per-org "send test notification" calls so the test route
	// cannot be looped to spam a configured recipient or probe public hosts.
	testLimiter *ratelimit.Limiter
	// monitorAlertLimiter caps monitor-failure alerts PER ORG, independent of
	// the per-monitor cap.
	//
	// The per-monitor key includes the slug, which the caller chooses, and
	// UpsertMonitorCheckin CREATES the monitor if it does not exist. So a DSN
	// public key could invent an unlimited number of slugs, each auto-registering
	// and each getting its own fresh per-monitor budget. Capping per (org, slug)
	// bounds an honestly-flapping cron and bounds nothing at all against someone
	// varying the slug.
	monitorAlertLimiter *ratelimit.Limiter

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

	// bgSlots bounds the detached goroutines ingest spawns per event. Ingest used
	// to fire one unbounded `go func` per event, so a single large envelope could
	// create tens of thousands of goroutines all queueing on a ~20-connection pgx
	// pool and starving every other tenant. Acquire is non-blocking: when the
	// pool is saturated the work is DROPPED rather than queued, because ingest
	// must never block and a backlog of stale alert evaluations has no value.
	//
	// Alerting and AI triage get SEPARATE pools on purpose. Sharing one meant a
	// burst of new fingerprints could fill every slot with 90-second BYOAI
	// completions, so 15-second alert evaluation, the thing that actually pages a
	// human, was dropped while the optional nice-to-have ran.
	bgSlots     map[string]chan struct{}
	bgSlotsOnce sync.Once
	// bgWG tracks in-flight background jobs so shutdown can wait for them.
	// Without it SIGTERM dropped alert dispatches that were mid-flight and
	// abandoned AI triage calls whose token budget had already been claimed.
	bgWG sync.WaitGroup
}

// WaitBackground blocks until every in-flight background job finishes or the
// context expires. Called during graceful shutdown, after the HTTP listener has
// stopped accepting, so a page that was already being dispatched still goes out.
func (s *Server) WaitBackground(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		s.bgWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		slog.Warn("shutdown: background jobs still running, giving up on them")
	}
}

// Background pool ceilings, sized against the ~20-connection pgx pool.
const (
	bgAlertWorkers  = 24
	bgTriageWorkers = 8
)

// bgPool returns the worker pool for a job class, lazily initialised so a Server
// built directly in a test (not via NewServer) still works instead of blocking
// forever on a nil channel.
func (s *Server) bgPool(name string) chan struct{} {
	s.bgSlotsOnce.Do(func() {
		if s.bgSlots == nil {
			s.bgSlots = map[string]chan struct{}{}
		}
		if s.bgSlots["auto-triage"] == nil {
			s.bgSlots["auto-triage"] = make(chan struct{}, bgTriageWorkers)
		}
		if s.bgSlots["default"] == nil {
			s.bgSlots["default"] = make(chan struct{}, bgAlertWorkers)
		}
	})
	if ch, ok := s.bgSlots[name]; ok {
		return ch
	}
	return s.bgSlots["default"]
}

// goBackground runs fn on a bounded worker slot with its own detached, deadlined
// context. Returns false (and drops fn) when every slot for that class is busy.
func (s *Server) goBackground(name string, timeout time.Duration, fn func(context.Context)) bool {
	pool := s.bgPool(name)
	select {
	case pool <- struct{}{}:
	default:
		slog.Warn("background work dropped: all worker slots busy", "job", name, "capacity", cap(pool))
		return false
	}
	s.bgWG.Add(1)
	go func() {
		defer s.bgWG.Done()
		defer func() { <-pool }()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("background job panicked", "job", name, "panic", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		fn(ctx)
	}()
	return true
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
		ai:           ai.New(!cfg.AllowPrivateAIEndpoint),
		secrets:      secretbox.New(cfg.SecretKey),

		loginLimiter:  ratelimit.New(loginFailBudget, loginFailWindow),
		ingestLimiter: ratelimit.New(cfg.IngestRatePerMin, time.Minute),
		mcpLimiter:    ratelimit.New(mcpRatePerMin, time.Minute),
		resetLimiter:  ratelimit.New(5, 15*time.Minute), // <=5 reset requests per (email, ip) / 15m
		testLimiter:   ratelimit.New(10, time.Minute),   // <=10 test-sends per org / min
		// <=20 monitor-failure alerts per ORG per minute, whatever the slug.
		// Above any real estate (a flapping fleet transitions a handful of
		// monitors a minute) and far below what a mailbox tolerates.
		monitorAlertLimiter: ratelimit.New(20, time.Minute),

		secProjects:      map[string]*generated.Project{},
		secIPLimiter:     ratelimit.New(1, 10*time.Second), // <=1 per (kind, ip) / 10s
		secGlobalLimiter: ratelimit.New(60, time.Minute),   // <=60 per kind / min

		bgSlots: map[string]chan struct{}{
			"default":     make(chan struct{}, bgAlertWorkers),
			"auto-triage": make(chan struct{}, bgTriageWorkers),
		},
	}
	// Persist each delivery outcome so a silently-failing channel becomes
	// visible (last_ok_at / last_error) instead of vanishing into a log line.
	srv.dispatcher.Recorder = srv.recordChannelDelivery
	return srv
}

// dispatchToProject sends one notification to the channels routed to a project,
// which is every channel routed to it PLUS every channel with no routing at all.
//
// Prefer this over dispatchToOrg wherever a project is known. Alerts are almost
// always about a project, and org-wide fan-out is what put a production incident
// and a side project in the same Slack.
func (s *Server) dispatchToProject(ctx context.Context, org, project string, n alerts.Notification) {
	chans, err := s.q.ListEnabledChannelsForProject(ctx, generated.ListEnabledChannelsForProjectParams{
		OrgID: org, ProjectID: project,
	})
	if err != nil || len(chans) == 0 {
		return
	}
	channels := make([]alerts.Channel, 0, len(chans))
	for _, c := range chans {
		channels = append(channels, alerts.Channel{ID: c.ID, OrgID: c.OrgID, Type: c.Type, Config: s.decryptChannelConfig(c.Type, c.Config)})
	}
	s.dispatcher.Dispatch(ctx, channels, n)
}

// dispatchToOrg fans out to EVERY enabled channel in the org, ignoring routing.
//
// Reserved for notifications that genuinely belong to no single project. Routing
// is per project, so a channel scoped to project A has no answer to "should this
// org-level message reach you"; delivering is the safe direction, because the
// alternative is an alert that silently reaches nobody once anyone configures
// routing. Deciding that once and writing it here is the point: the recurring
// defect in this codebase is a rule applied to some call sites and not others.
//
// If you are reaching for this and you have a project id, use dispatchToProject.
func (s *Server) dispatchToOrg(ctx context.Context, org string, n alerts.Notification) {
	chans, err := s.q.ListEnabledNotificationChannelsByOrg(ctx, org)
	if err != nil || len(chans) == 0 {
		return
	}
	channels := make([]alerts.Channel, 0, len(chans))
	for _, c := range chans {
		channels = append(channels, alerts.Channel{ID: c.ID, OrgID: c.OrgID, Type: c.Type, Config: s.decryptChannelConfig(c.Type, c.Config)})
	}
	s.dispatcher.Dispatch(ctx, channels, n)
}

// recordChannelDelivery persists the outcome of one notification delivery
// attempt. Best-effort: a failed write must never affect alert dispatch. Runs
// in the dispatch goroutine (ingest/watchdog) or the test-send request.
func (s *Server) recordChannelDelivery(ctx context.Context, orgID, channelID string, derr error) {
	// Detach from the dispatch context, which a prior slow/hung channel in the
	// same org may already have cancelled, with a fresh short deadline. Without
	// this, the exact outcome the feature exists to record - a delivery timeout
	// - could itself fail to persist ("context canceled").
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
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
