-- name: UpsertSourceMap :one
INSERT INTO source_map_artifacts (id, project_id, org_id, release, name, content)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (project_id, release, name) DO UPDATE
  SET content = EXCLUDED.content, created_at = now()
RETURNING id, project_id, org_id, release, name, length(content) AS size, created_at;

-- name: ListSourceMapsByProject :many
SELECT id, release, name, length(content) AS size, created_at
FROM source_map_artifacts
WHERE project_id = $1 AND org_id = $2
ORDER BY release DESC, name ASC;

-- name: GetSourceMapsForRelease :many
-- Bounded: symbolication loads full content into memory, so cap the number of
-- maps pulled per release to keep triage from exhausting memory.
SELECT name, content
FROM source_map_artifacts
WHERE project_id = $1 AND org_id = $2 AND release = $3
ORDER BY created_at DESC
LIMIT 1000;

-- name: DeleteSourceMap :execrows
DELETE FROM source_map_artifacts WHERE id = $1 AND project_id = $2 AND org_id = $3;
