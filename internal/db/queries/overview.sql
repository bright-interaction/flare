-- Org-wide dashboard aggregates for the Overview page. Every query is scoped
-- by org_id (the tenant boundary); they intentionally span all of the org's
-- projects, so each carries a ciguard allow-no-project marker directly above
-- its -- name: directive.

-- ciguard:allow-no-project org-wide dashboard aggregate (scoped by org_id)
-- name: OverviewEventCount24h :one
SELECT count(*) FROM events
WHERE org_id = $1 AND received_at >= now() - interval '24 hours';

-- ciguard:allow-no-project org-wide dashboard aggregate (scoped by org_id)
-- name: OverviewUnresolvedCount :one
SELECT count(*) FROM issues
WHERE org_id = $1 AND status = 'unresolved';

-- ciguard:allow-no-project org-wide dashboard aggregate (scoped by org_id)
-- name: OverviewNewIssuesToday :one
SELECT count(*) FROM issues
WHERE org_id = $1 AND first_seen >= date_trunc('day', now());

-- ciguard:allow-no-project org-wide dashboard aggregate (scoped by org_id)
-- name: OverviewEventVolumeByHour :many
SELECT date_trunc('hour', received_at)::timestamptz AS hour, count(*) AS count
FROM events
WHERE org_id = $1 AND received_at >= now() - interval '24 hours'
GROUP BY hour
ORDER BY hour;

-- ciguard:allow-no-project org-wide dashboard aggregate (scoped by org_id)
-- name: OverviewTopIssues :many
SELECT i.id, i.title, i.level, i.event_count, i.project_id, p.name AS project_name
FROM issues i
JOIN projects p ON p.id = i.project_id
WHERE i.org_id = $1 AND i.status = 'unresolved'
ORDER BY i.event_count DESC, i.last_seen DESC
LIMIT 8;

-- ciguard:allow-no-project org-wide dashboard aggregate (scoped by org_id)
-- name: OverviewProjectVolumeByHour :many
SELECT project_id, date_trunc('hour', received_at)::timestamptz AS hour, count(*) AS count
FROM events
WHERE org_id = $1 AND received_at >= now() - interval '24 hours'
GROUP BY project_id, hour
ORDER BY project_id, hour;

-- ciguard:allow-no-project org-wide dashboard aggregate (scoped by org_id)
-- name: OverviewProjectUnresolved :many
SELECT project_id, count(*) AS count
FROM issues
WHERE org_id = $1 AND status = 'unresolved'
GROUP BY project_id;
