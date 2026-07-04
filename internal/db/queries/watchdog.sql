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
SELECT count(*) FROM events
WHERE project_id = $1 AND org_id = $2 AND received_at >= $3;

-- name: TrySetAlertRuleFired :execrows
-- Test-and-set cooldown: claims the alert for this rule only when it has not
-- fired since $3 (now - cooldown). Returns 1 when claimed, 0 when still cooling.
UPDATE alert_rules SET last_fired_at = now()
WHERE id = $1 AND org_id = $2 AND (last_fired_at IS NULL OR last_fired_at < $3);
