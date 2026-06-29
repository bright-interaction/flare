-- name: InsertLogs :copyfrom
INSERT INTO logs (id, project_id, org_id, severity, body, attributes, trace_id, span_id, observed_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: SearchLogs :many
SELECT * FROM logs
WHERE project_id = $1
  AND org_id = $2
  AND (sqlc.narg(severity)::text IS NULL OR severity = sqlc.narg(severity))
  AND (sqlc.narg(q)::text IS NULL OR body ILIKE '%' || sqlc.narg(q) || '%')
  AND (sqlc.narg(trace_id)::text IS NULL OR trace_id = sqlc.narg(trace_id))
  AND (sqlc.narg(since)::timestamptz IS NULL OR observed_at >= sqlc.narg(since))
ORDER BY observed_at DESC
LIMIT $3;
