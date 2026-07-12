-- Watchdog worker queries. The worker runs across all orgs/projects, so
-- ListWatchdogRules is intentionally not project-scoped; the per-project reads
-- it drives (CountEventsForProjectSince) carry the project_id + org_id predicate.

-- ciguard:allow-unscoped global watchdog worker; every downstream read (CountEventsForProjectSince) re-scopes by project_id AND org_id, and TrySetAlertRuleFired filters org_id
-- name: ListWatchdogRules :many
-- Enabled anomaly/silence rules with the project name + org for dispatch.
SELECT r.id, r.project_id, r.org_id, r.name, r.type, r.threshold, r.window_minutes,
       r.last_fired_at, p.name AS project_name
FROM alert_rules r
JOIN projects p ON p.id = r.project_id
WHERE r.enabled = true AND r.type IN ('anomaly', 'silence');

-- name: CountEventsForProjectSince :one
-- EVERY event, including info/debug. This is the SILENCE count, and it must stay
-- unfiltered: the info-level "service-up" heartbeat is precisely the signal that
-- proves a healthy, low-error service is still alive.
SELECT count(*) FROM events
WHERE project_id = $1 AND org_id = $2 AND received_at >= $3;

-- name: CountActionableEventsForProjectSince :one
-- Only ACTIONABLE events. This is the ANOMALY count.
--
-- An "error-rate anomaly" must not fire on heartbeat volume. Both rules used to
-- share the unfiltered count above, so a project going from silent to
-- heartbeating looked like an infinite error spike against its own zero
-- baseline: the entire estate tripped this rule on 2026-07-11 (the day the
-- heartbeat shipped), and mesh-hub + mesh-cloud tripped it again the moment they
-- were first instrumented. Heartbeats are liveness, not errors.
SELECT count(*) FROM events
WHERE project_id = $1 AND org_id = $2 AND received_at >= $3
  AND level NOT IN ('info', 'debug');

-- name: TrySetAlertRuleFired :execrows
-- Test-and-set cooldown: claims the alert for this rule only when it has not
-- fired since $3 (now - cooldown). Returns 1 when claimed, 0 when still cooling.
UPDATE alert_rules SET last_fired_at = now()
WHERE id = $1 AND org_id = $2 AND (last_fired_at IS NULL OR last_fired_at < $3);
