package api

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bright-interaction/flare/internal/alerts"
	"github.com/bright-interaction/flare/internal/db/generated"
)

// anomalyMinEvents is the absolute floor for an anomaly alert. Without it, a
// normally-silent project whose baseline is ~0 would look like an infinite
// deviation on its first few errors and page constantly. A real anomaly worth
// waking someone for clears this AND the baseline multiplier.
const anomalyMinEvents = 5

// monitorRenotifySeconds is how long a monitor stays claimed as "missing"
// before it is eligible to page again. Without a re-notify window, the claim is
// taken before dispatch, so one dropped delivery silences a dead cron forever.
const monitorRenotifySeconds int64 = 24 * 60 * 60

// RunWatchdog periodically evaluates the "anomaly" and "silence" alert rules,
// which cannot ride on ingest: silence is the ABSENCE of events, and anomaly
// needs a baseline window. It reuses the same notification channels + dispatch
// as issue alerts. Best-effort throughout: a bad rule logs and is skipped, the
// loop never dies.
func (s *Server) RunWatchdog(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	slog.Info("watchdog started", "interval", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// The tick runs in its own goroutine guarded by an in-flight flag, so a slow
	// or hung dispatch (mail, webhook, Slack) can never park the ticker itself.
	// Overlapping ticks are skipped rather than queued: the work is idempotent
	// and claim-gated, so skipping is always safer than piling up.
	var inFlight atomic.Bool
	var skipped atomic.Int64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !inFlight.CompareAndSwap(false, true) {
				if n := skipped.Add(1); n%10 == 1 {
					slog.Warn("watchdog: previous tick still running, skipping", "skipped_total", n)
				}
				continue
			}
			go func() {
				defer inFlight.Store(false)
				// A panic in a detached goroutine takes the whole process down,
				// and this one is the alerting loop. Contain it.
				defer func() {
					if r := recover(); r != nil {
						slog.Error("watchdog: tick panicked", "panic", r)
					}
				}()
				s.watchdogTick(ctx)
			}()
		}
	}
}

func (s *Server) watchdogTick(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Cache channels per org so N rules/monitors in one org do one channel load.
	chanCache := map[string][]alerts.Channel{}
	now := time.Now()

	// Anomaly/silence rules. A list error is logged but not fatal to the tick:
	// monitor checking below is independent and must still run.
	rules, err := s.q.ListWatchdogRules(ctx)
	if err != nil {
		slog.Error("watchdog: list rules failed", "error", err)
	}

	for _, rule := range rules {
		reason := s.watchdogReason(ctx, rule, now)
		if reason == "" {
			continue
		}
		// Load channels BEFORE claiming the cooldown. A firing condition on an
		// org with no channel (or a transient channel-load error) must not
		// spend the cooldown: otherwise the first real alert is suppressed for
		// up to an hour after a channel is finally added.
		channels := s.orgChannels(ctx, rule.OrgID, chanCache)
		if len(channels) == 0 {
			continue
		}
		// Cooldown dedup: claim the fire only once a notification will actually
		// be dispatched. max(window, 60m) so a persisting condition pages once.
		cooldown := time.Duration(maxInt32(rule.WindowMinutes, 60)) * time.Minute
		claimed, err := s.q.TrySetAlertRuleFired(ctx, generated.TrySetAlertRuleFiredParams{
			ID:          rule.ID,
			OrgID:       rule.OrgID,
			LastFiredAt: pgtype.Timestamptz{Time: now.Add(-cooldown), Valid: true},
		})
		if err != nil || claimed == 0 {
			continue
		}
		s.dispatcher.Dispatch(ctx, channels, alerts.Notification{
			ProjectName: rule.ProjectName,
			Title:       rule.ProjectName,
			Level:       "warning",
			Reason:      reason,
			URL:         strings.TrimRight(s.cfg.BaseURL, "/") + "/projects/" + rule.ProjectID,
		})
		slog.Info("watchdog: alert fired", "type", rule.Type, "project", rule.ProjectName, "reason", reason)
	}

	// Overdue cron/check-in monitors: a scheduled job that stopped pinging.
	s.checkMonitors(ctx, chanCache, now)
}

// checkMonitors fires a "missing" alert for each configured monitor whose
// check-in is overdue (interval + grace elapsed). Claims each atomically via
// TrySetMonitorMissing so exactly one replica pages, and only once until the
// job checks in again (which flips state back to ok).
func (s *Server) checkMonitors(ctx context.Context, chanCache map[string][]alerts.Channel, now time.Time) {
	due, err := s.q.ListDueMonitors(ctx, generated.ListDueMonitorsParams{
		Column1:         pgtype.Timestamptz{Time: now, Valid: true},
		RenotifySeconds: monitorRenotifySeconds,
	})
	if err != nil {
		slog.Error("watchdog: list due monitors failed", "error", err)
		return
	}
	for _, m := range due {
		channels := s.orgChannels(ctx, m.OrgID, chanCache)
		if len(channels) == 0 {
			continue
		}
		claimed, err := s.q.TrySetMonitorMissing(ctx, generated.TrySetMonitorMissingParams{
			ID:              m.ID,
			OrgID:           m.OrgID,
			LastAlertAt:     pgtype.Timestamptz{Time: now, Valid: true},
			RenotifySeconds: monitorRenotifySeconds,
		})
		if err != nil || claimed == 0 {
			continue
		}
		// Mirror the query's COALESCE: a monitor that has never checked in is
		// measured from creation, and says so, rather than reporting a nonsense
		// age off a zero-value timestamp.
		since, reason := m.LastPingAt.Time, "Check-in missing"
		if !m.LastPingAt.Valid {
			since, reason = m.CreatedAt.Time, "Never checked in since it was created"
		}
		overdue := int64(now.Sub(since).Seconds())
		s.dispatcher.Dispatch(ctx, channels, alerts.Notification{
			ProjectName: m.ProjectName,
			Title:       "Monitor overdue: " + m.Slug,
			Level:       "warning",
			Reason:      fmt.Sprintf("%s: nothing for ~%ds (expected every %ds + %ds grace)", reason, overdue, m.IntervalSeconds, m.GraceSeconds),
			URL:         strings.TrimRight(s.cfg.BaseURL, "/") + "/projects/" + m.ProjectID,
		})
		slog.Info("watchdog: monitor missing", "monitor", m.Slug, "project", m.ProjectName)
	}
}

// orgChannels returns an org's enabled notification channels, cached per tick.
// A load error caches an empty slice so the rule is skipped (never dispatched
// to nothing, never a spent cooldown) and retried on the next tick.
func (s *Server) orgChannels(ctx context.Context, org string, cache map[string][]alerts.Channel) []alerts.Channel {
	if channels, ok := cache[org]; ok {
		return channels
	}
	chans, err := s.q.ListEnabledNotificationChannelsByOrg(ctx, org)
	if err != nil {
		slog.Error("watchdog: load channels failed", "org", org, "error", err)
		cache[org] = nil
		return nil
	}
	channels := make([]alerts.Channel, 0, len(chans))
	for _, c := range chans {
		channels = append(channels, alerts.Channel{ID: c.ID, OrgID: c.OrgID, Type: c.Type, Config: s.decryptChannelConfig(c.Type, c.Config)})
	}
	cache[org] = channels
	return channels
}

// watchdogReason evaluates one rule against live counts and returns a non-empty
// reason when it should fire. It fetches the two windows it needs, then defers
// to the pure decision functions (tested without a DB).
// The two rule types deliberately count DIFFERENT things:
//
//	anomaly  counts only actionable events (error/warning/fatal). An
//	         "error-rate anomaly" that counted info-level heartbeats would fire
//	         on liveness traffic, which is exactly what happened: the whole
//	         estate tripped it on 2026-07-11 when the heartbeat shipped, and
//	         mesh-hub + mesh-cloud tripped it again the moment they were first
//	         instrumented (0 events -> a steady beacon reads as an infinite
//	         spike against the project's own zero baseline).
//
//	silence  counts EVERY event, heartbeats included. That is the whole point:
//	         the beacon is what proves a healthy, error-free service is alive, so
//	         filtering it out would make every quiet-but-healthy service look dead.
func (s *Server) watchdogReason(ctx context.Context, rule *generated.ListWatchdogRulesRow, now time.Time) string {
	if rule.WindowMinutes < 1 {
		return ""
	}
	windowStart := now.Add(-time.Duration(rule.WindowMinutes) * time.Minute)
	dayStart := now.Add(-24 * time.Hour)

	switch rule.Type {
	case "anomaly":
		recent, err := s.countActionableEventsSince(ctx, rule.ProjectID, rule.OrgID, windowStart)
		if err != nil {
			// Swallowing this silently disabled every anomaly and silence alert
			// for as long as the count query kept failing, with nothing in the
			// logs to say so.
			slog.Error("watchdog: baseline count failed, rule skipped",
				"rule", rule.ID, "type", rule.Type, "project", rule.ProjectID, "error", err)
			return ""
		}
		day, err := s.countActionableEventsSince(ctx, rule.ProjectID, rule.OrgID, dayStart)
		if err != nil {
			slog.Error("watchdog: baseline count failed, rule skipped",
				"rule", rule.ID, "type", rule.Type, "project", rule.ProjectID, "error", err)
			return ""
		}
		return anomalyReason(recent, day, rule.WindowMinutes, rule.Threshold)
	case "silence":
		recent, err := s.countEventsSince(ctx, rule.ProjectID, rule.OrgID, windowStart)
		if err != nil {
			slog.Error("watchdog: baseline count failed, rule skipped",
				"rule", rule.ID, "type", rule.Type, "project", rule.ProjectID, "error", err)
			return ""
		}
		day, err := s.countEventsSince(ctx, rule.ProjectID, rule.OrgID, dayStart)
		if err != nil {
			slog.Error("watchdog: baseline count failed, rule skipped",
				"rule", rule.ID, "type", rule.Type, "project", rule.ProjectID, "error", err)
			return ""
		}
		return silenceReason(recent, day, rule.Threshold)
	}
	return ""
}

// countEventsSince counts EVERY event (heartbeats included). Silence only.
func (s *Server) countEventsSince(ctx context.Context, projectID, org string, since time.Time) (int64, error) {
	return s.q.CountEventsForProjectSince(ctx, generated.CountEventsForProjectSinceParams{
		ProjectID:  projectID,
		OrgID:      org,
		ReceivedAt: pgtype.Timestamptz{Time: since, Valid: true},
	})
}

// countActionableEventsSince excludes info/debug (heartbeats, breadcrumbs).
// Anomaly only.
func (s *Server) countActionableEventsSince(ctx context.Context, projectID, org string, since time.Time) (int64, error) {
	return s.q.CountActionableEventsForProjectSince(ctx, generated.CountActionableEventsForProjectSinceParams{
		ProjectID:  projectID,
		OrgID:      org,
		ReceivedAt: pgtype.Timestamptz{Time: since, Valid: true},
	})
}

// anomalyReason fires when the recent window's event count both exceeds the
// absolute floor AND is at least `multiplier` times the project's own trailing
// baseline rate (average per same-sized window over the last 24h, excluding the
// recent window). multiplier is the rule threshold; a missing/<2 multiplier
// defaults to 3.
func anomalyReason(recent, day24h int64, windowMin, multiplier int32) string {
	if windowMin < 1 || recent < anomalyMinEvents {
		return ""
	}
	if multiplier < 2 {
		multiplier = 3
	}
	// day24h is a strict superset of recent; clamp defensively so a skew can
	// never produce a negative baseline (and a nonsensical alert message).
	if recent > day24h {
		day24h = recent
	}
	windowsPerDay := (24 * 60) / int64(windowMin)
	if windowsPerDay < 2 {
		return "" // window too coarse (>12h) to have a baseline
	}
	baseline := float64(day24h-recent) / float64(windowsPerDay-1)
	threshold := float64(multiplier) * baseline
	if threshold < anomalyMinEvents {
		threshold = anomalyMinEvents
	}
	if float64(recent) < threshold {
		return ""
	}
	if baseline <= 0 {
		// Near-silent baseline: the multiplier is undefined, so report the
		// burst plainly rather than presenting the raw count as an "Nx".
		return fmt.Sprintf("Anomaly: %d events in %dm from a near-silent baseline", recent, windowMin)
	}
	return fmt.Sprintf("Anomaly: %d events in %dm, ~%.0f/window baseline (%.1fx)",
		recent, windowMin, baseline, float64(recent)/baseline)
}

// silenceReason fires when a normally-active project (>= minActive events over
// the last 24h) has gone completely quiet in the recent window. minActive is
// the rule threshold; a missing/<1 threshold defaults to 20.
func silenceReason(recent, day24h int64, minActive int32) string {
	if recent != 0 {
		return ""
	}
	if minActive < 1 {
		minActive = 20
	}
	if day24h < int64(minActive) {
		return "" // not normally active; silence is expected
	}
	return fmt.Sprintf("Silence: no events recently, but ~%d in the last 24h", day24h)
}

func maxInt32(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}
