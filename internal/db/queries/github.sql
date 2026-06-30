-- name: UpsertGithubConfig :exec
INSERT INTO github_config (org_id, repo, token)
VALUES ($1, $2, $3)
ON CONFLICT (org_id) DO UPDATE
  SET repo = EXCLUDED.repo, token = EXCLUDED.token, created_at = now();

-- name: GetGithubConfig :one
SELECT * FROM github_config WHERE org_id = $1;

-- name: DeleteGithubConfig :exec
DELETE FROM github_config WHERE org_id = $1;

-- ciguard:allow-no-project write by project-unique issue id
-- name: SetIssueGithubURL :execrows
UPDATE issues SET github_url = $3 WHERE id = $1 AND org_id = $2;
