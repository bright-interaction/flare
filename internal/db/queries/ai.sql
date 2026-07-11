-- name: UpsertAIConfig :exec
INSERT INTO ai_config (org_id, base_url, api_key, model, format, enabled, auto_triage, triage_daily_budget)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (org_id) DO UPDATE
  SET base_url = EXCLUDED.base_url, api_key = EXCLUDED.api_key,
      model = EXCLUDED.model, format = EXCLUDED.format, enabled = EXCLUDED.enabled,
      auto_triage = EXCLUDED.auto_triage, triage_daily_budget = EXCLUDED.triage_daily_budget;

-- ciguard:allow-no-project per-org daily AI usage counter keyed by org_id; the claim below filters org_id
-- name: ClaimAITriageBudget :one
-- Atomically claim one triage against the org's daily budget. Returns the new
-- count when under budget; no row (pgx.ErrNoRows) when the budget is reached.
INSERT INTO ai_triage_usage (org_id, day, triage_count)
VALUES (@org_id, CURRENT_DATE, 1)
ON CONFLICT (org_id, day) DO UPDATE
  SET triage_count = ai_triage_usage.triage_count + 1
  WHERE ai_triage_usage.triage_count < @budget
RETURNING triage_count;

-- name: GetAIConfig :one
SELECT * FROM ai_config WHERE org_id = $1;

-- name: DeleteAIConfig :exec
DELETE FROM ai_config WHERE org_id = $1;

-- ciguard:allow-no-project write by project-unique issue id
-- name: SetIssueTriage :exec
UPDATE issues SET ai_triage = $3, ai_triaged_at = now() WHERE id = $1 AND org_id = $2;
