-- name: InsertLogs :copyfrom
INSERT INTO logs (id, project_id, org_id, severity, body, attributes, trace_id, span_id, observed_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: SearchLogs :many
SELECT * FROM logs
WHERE project_id = $1
  AND org_id = $2
  AND (sqlc.narg(severity)::text IS NULL OR severity = sqlc.narg(severity))
  -- ESCAPE '\' matches the issues pillar. Without it the backslashes escapeLike
  -- writes are literal characters, so a term containing % or _ still matches as
  -- a wildcard and an operator searching for "50%" or "user_id" gets silently
  -- wrong rows. The escaping fix reached the issues queries and never reached
  -- this one.
  AND (sqlc.narg(q)::text IS NULL OR body ILIKE '%' || sqlc.narg(q) || '%' ESCAPE '\')
  AND (sqlc.narg(trace_id)::text IS NULL OR trace_id = sqlc.narg(trace_id))
  AND (sqlc.narg(since)::timestamptz IS NULL OR observed_at >= sqlc.narg(since))
  -- Keyset paging, NOT OFFSET. Logs stream in continuously, so an OFFSET over a
  -- DESC observed_at ordering skips and duplicates rows as new ones land: page 2
  -- is computed against a list that grew since page 1. Paging from the last row
  -- seen is stable under concurrent ingest.
  AND (sqlc.narg(before)::timestamptz IS NULL OR observed_at < sqlc.narg(before))
ORDER BY observed_at DESC
LIMIT $3;
