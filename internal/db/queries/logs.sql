-- name: InsertLogs :copyfrom
INSERT INTO logs (id, project_id, org_id, severity, body, attributes, trace_id, span_id, observed_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: SearchLogs :many
SELECT * FROM logs
WHERE project_id = $1
  AND org_id = $2
  AND (sqlc.narg(severity)::text IS NULL OR severity = sqlc.narg(severity))
  -- ESCAPE '\' matches the issues pillar. It is a redundant restatement of
  -- PostgreSQL's default (backslash is already the LIKE escape character), kept
  -- so the dependency is explicit rather than inherited. Verified against a
  -- real postgres:16 on 2026-08-11: with and without the clause, escapeLike's
  -- output behaves identically. The defect finding M1 actually named here was
  -- Go-side, not SQL-side; see internal/db/queries/events.sql for the full
  -- correction.
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
