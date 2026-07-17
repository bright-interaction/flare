-- name: CreateProject :one
INSERT INTO projects (id, org_id, name, slug, platform, public_key, dsn_id)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: ListProjectsByOrg :many
SELECT * FROM projects
WHERE org_id = $1
ORDER BY created_at DESC;

-- ciguard:allow-unscoped ingest path keyed by the unguessable public_key (the DSN secret)
-- name: GetProjectByPublicKey :one
SELECT * FROM projects WHERE public_key = $1;

-- name: GetProjectByID :one
-- Tenant scope is MANDATORY: an empty org_scope now matches nothing (fail closed) rather
-- than returning any org's project (which leaks the project's ingest public_key/DSN). No
-- caller needs the unscoped form; a genuine global lookup would use a separate named query.
SELECT * FROM projects
WHERE id = $1
  AND org_id = sqlc.arg(org_scope);

-- name: GetProjectBySlug :one
SELECT * FROM projects WHERE org_id = $1 AND slug = $2;

-- ciguard:allow-unscoped /go/{dsnID} redirect resolves numeric dsn_id; target page requires a session
-- name: GetProjectByDsnID :one
SELECT * FROM projects WHERE dsn_id = $1;

-- name: DeleteProject :execrows
-- Cascades to issues + alert_rules via ON DELETE CASCADE. events/logs/spans
-- have no FK (partitioned hot tables), so the handler deletes those explicitly
-- in the same transaction first.
DELETE FROM projects WHERE id = $1 AND org_id = $2;

-- name: DeleteProjectEvents :exec
DELETE FROM events WHERE project_id = $1 AND org_id = $2;

-- name: DeleteProjectLogs :exec
DELETE FROM logs WHERE project_id = $1 AND org_id = $2;

-- name: DeleteProjectSpans :exec
DELETE FROM spans WHERE project_id = $1 AND org_id = $2;

-- name: DeleteProjectMetrics :exec
DELETE FROM metrics WHERE project_id = $1 AND org_id = $2;
