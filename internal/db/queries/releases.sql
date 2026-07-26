-- name: UpsertRelease :exec
-- Get-or-create a release. Called from ingest (auto) and the deploy-marker
-- endpoint. created_at is preserved across calls (the first sighting wins).
INSERT INTO releases (id, project_id, org_id, version)
VALUES ($1, $2, $3, $4)
ON CONFLICT (project_id, version) DO NOTHING;

-- name: ListReleasesByProject :many
-- Releases newest first, with the count of issues first seen in each.
SELECT r.version,
       r.created_at,
       (SELECT count(*) FROM issues i
         WHERE i.project_id = r.project_id AND i.org_id = r.org_id
           AND i.first_release = r.version) AS new_issues
FROM releases r
WHERE r.project_id = $1 AND r.org_id = $2
ORDER BY r.created_at DESC
LIMIT 200;
