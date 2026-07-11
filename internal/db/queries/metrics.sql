-- name: InsertMetrics :copyfrom
INSERT INTO metrics (id, project_id, org_id, name, kind, value, labels, observed_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: ListMetricNames :many
-- Distinct metric names for a project in the last 24h, with a point count, for
-- the metrics browser.
SELECT name,
       max(kind)::text AS kind,
       count(*) AS points,
       max(observed_at)::timestamptz AS last_seen
FROM metrics
WHERE project_id = $1 AND org_id = $2 AND observed_at > now() - interval '24 hours'
GROUP BY name
ORDER BY name;

-- name: QueryMetricSeries :many
-- Raw points for one metric name over a window, newest first, capped. The UI
-- reverses them for a time-ordered line chart.
SELECT value, labels, observed_at
FROM metrics
WHERE project_id = $1 AND org_id = $2 AND name = $3 AND observed_at > $4
ORDER BY observed_at DESC
LIMIT $5;
