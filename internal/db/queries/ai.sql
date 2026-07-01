-- name: UpsertAIConfig :exec
INSERT INTO ai_config (org_id, base_url, api_key, model, format, enabled)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (org_id) DO UPDATE
  SET base_url = EXCLUDED.base_url, api_key = EXCLUDED.api_key,
      model = EXCLUDED.model, format = EXCLUDED.format, enabled = EXCLUDED.enabled;

-- name: GetAIConfig :one
SELECT * FROM ai_config WHERE org_id = $1;

-- name: DeleteAIConfig :exec
DELETE FROM ai_config WHERE org_id = $1;

-- ciguard:allow-no-project write by project-unique issue id
-- name: SetIssueTriage :exec
UPDATE issues SET ai_triage = $3, ai_triaged_at = now() WHERE id = $1 AND org_id = $2;
