-- name: UpsertIssue :one
-- Groups an incoming event into its issue. is_new distinguishes a freshly
-- created issue (first_seen == last_seen on insert) from a recurrence. Status
-- is intentionally left untouched on conflict so RETURNING exposes the PRIOR
-- status: the caller reopens a 'resolved' issue and fires a regression alert.
INSERT INTO issues (id, project_id, org_id, fingerprint, title, culprit, level, platform, first_release, first_seen, last_seen, event_count)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now(), now(), 1)
ON CONFLICT (project_id, fingerprint) DO UPDATE
SET last_seen    = now(),
    event_count  = issues.event_count + 1,
    level        = EXCLUDED.level,
    title        = EXCLUDED.title,
    culprit      = EXCLUDED.culprit
RETURNING id, project_id, org_id, fingerprint, title, culprit, level, status, platform,
          first_release, first_seen, last_seen, event_count, (first_seen = last_seen) AS is_new;

-- ciguard:allow-no-project reopen by project-unique issue id
-- name: ReopenIssue :exec
UPDATE issues SET status = 'unresolved' WHERE id = $1 AND org_id = $2;

-- ciguard:allow-no-project count by project-unique issue id (spike rule window)
-- name: CountEventsForIssueSince :one
SELECT count(*) FROM events WHERE issue_id = $1 AND org_id = $2 AND received_at >= $3;

-- ciguard:allow-no-project atomic spike dedup by project-unique issue id
-- name: TrySetIssueSpike :execrows
-- Test-and-set: claims the spike alert for this issue only when the last one is
-- older than the window cutoff ($3). Returns 1 row when claimed, 0 when another
-- event already fired within the window.
UPDATE issues SET last_spike_at = now()
WHERE id = $1 AND org_id = $2 AND (last_spike_at IS NULL OR last_spike_at < $3);

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

-- ciguard:allow-no-project issue id is project-unique (one issue belongs to one project)
-- name: GetIssue :one
SELECT * FROM issues WHERE id = $1 AND org_id = $2;

-- ciguard:allow-no-project issue_id is project-unique, so events under it are one project's
-- name: ListEventsByIssue :many
SELECT * FROM events WHERE issue_id = $1 AND org_id = $2 ORDER BY received_at DESC LIMIT $3;

-- ciguard:allow-no-project issue_id is project-unique
-- name: GetLatestEvent :one
SELECT * FROM events WHERE issue_id = $1 AND org_id = $2 ORDER BY received_at DESC LIMIT 1;

-- ciguard:allow-no-project write by project-unique issue id
-- name: UpdateIssueStatus :one
UPDATE issues SET status = $3 WHERE id = $1 AND org_id = $2 RETURNING *;

-- ciguard:allow-no-project set-once flag by project-unique issue id
-- name: MarkIssueSensitive :exec
-- First detection wins: only set when not already flagged, so the badge is
-- stable and one write per issue, not per event.
UPDATE issues SET sensitive = $3 WHERE id = $1 AND org_id = $2 AND sensitive = '';
