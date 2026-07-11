-- name: UpsertMonitorCheckin :one
-- Records a check-in. Creates the monitor on first ping with an unconfigured
-- interval, otherwise refreshes the last ping. The caller passes the computed
-- state so a successful ping clears a prior missing or failed state.
INSERT INTO monitors (id, org_id, project_id, slug, last_ping_at, last_status, state)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (project_id, slug) DO UPDATE
SET last_ping_at = EXCLUDED.last_ping_at,
    last_status = EXCLUDED.last_status,
    state = EXCLUDED.state
RETURNING *;

-- name: GetMonitorBySlug :one
SELECT * FROM monitors WHERE project_id = $1 AND slug = $2;

-- name: GetMonitor :one
SELECT * FROM monitors WHERE id = $1 AND org_id = $2;

-- name: ListMonitorsByProject :many
SELECT * FROM monitors WHERE project_id = $1 AND org_id = $2 ORDER BY slug;

-- name: CreateMonitor :one
INSERT INTO monitors (id, org_id, project_id, slug, name, interval_seconds, grace_seconds)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: UpdateMonitorConfig :one
UPDATE monitors
SET name = $3, interval_seconds = $4, grace_seconds = $5
WHERE id = $1 AND org_id = $2
RETURNING *;

-- name: DeleteMonitor :execrows
DELETE FROM monitors WHERE id = $1 AND org_id = $2;

-- ciguard:allow-unscoped global watchdog worker scanning every org's monitors; the claim it drives (TrySetMonitorMissing) filters id AND org_id
-- name: ListDueMonitors :many
-- Configured monitors whose last ping is older than interval plus grace and
-- which are not already flagged missing. Joined to the project name for the
-- alert body. The watchdog claims each via TrySetMonitorMissing before
-- dispatching, so a stuck monitor pages once.
SELECT m.*, p.name AS project_name
FROM monitors m
JOIN projects p ON p.id = m.project_id
WHERE m.interval_seconds > 0
  AND m.state <> 'missing'
  AND m.last_ping_at IS NOT NULL
  AND EXTRACT(EPOCH FROM ($1::timestamptz - m.last_ping_at)) > (m.interval_seconds + m.grace_seconds);

-- name: TrySetMonitorMissing :execrows
-- Atomic claim: flip to missing only if still overdue and not already missing.
-- Exactly one replica's UPDATE affects a row, so the alert fires once.
UPDATE monitors
SET state = 'missing', last_alert_at = $3
WHERE id = $1 AND org_id = $2 AND state <> 'missing'
  AND last_ping_at IS NOT NULL
  AND EXTRACT(EPOCH FROM ($3::timestamptz - last_ping_at)) > (interval_seconds + grace_seconds);
