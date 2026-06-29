-- name: UpsertIssue :one
-- Groups an incoming event into its issue. is_new distinguishes a freshly
-- created issue (first_seen == last_seen on insert) from a recurrence, so the
-- caller can fire a new-issue alert. A resolved issue that recurs reopens.
INSERT INTO issues (id, project_id, org_id, fingerprint, title, culprit, level, platform, first_seen, last_seen, event_count)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now(), now(), 1)
ON CONFLICT (project_id, fingerprint) DO UPDATE
SET last_seen    = now(),
    event_count  = issues.event_count + 1,
    level        = EXCLUDED.level,
    title        = EXCLUDED.title,
    culprit      = EXCLUDED.culprit,
    status       = CASE WHEN issues.status = 'resolved' THEN 'unresolved' ELSE issues.status END
RETURNING id, project_id, org_id, fingerprint, title, culprit, level, status, platform,
          first_seen, last_seen, event_count, (first_seen = last_seen) AS is_new;

-- name: InsertEvent :exec
INSERT INTO events (id, project_id, org_id, issue_id, level, message, exception_type, exception_value, platform, environment, release, stacktrace, payload, received_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, now());

-- name: ListIssues :many
SELECT * FROM issues
WHERE project_id = $1
  AND org_id = $2
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
ORDER BY last_seen DESC
LIMIT $3 OFFSET $4;

-- name: CountIssues :one
SELECT count(*) FROM issues WHERE project_id = $1 AND org_id = $2;

-- name: GetIssue :one
SELECT * FROM issues WHERE id = $1 AND org_id = $2;

-- name: ListEventsByIssue :many
SELECT * FROM events WHERE issue_id = $1 AND org_id = $2 ORDER BY received_at DESC LIMIT $3;

-- name: GetLatestEvent :one
SELECT * FROM events WHERE issue_id = $1 AND org_id = $2 ORDER BY received_at DESC LIMIT 1;

-- name: UpdateIssueStatus :one
UPDATE issues SET status = $3 WHERE id = $1 AND org_id = $2 RETURNING *;
