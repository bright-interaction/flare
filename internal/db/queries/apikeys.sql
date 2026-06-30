-- name: CreateAPIKey :one
INSERT INTO api_keys (id, org_id, name, key_hash, key_prefix, expires_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- ciguard:allow-unscoped auth path keyed by the secret key_hash; org_id is the result, not the filter
-- name: GetAPIKeyByHash :one
SELECT * FROM api_keys WHERE key_hash = $1;

-- ciguard:allow-unscoped touches a single row already resolved by key_hash above
-- name: TouchAPIKey :exec
UPDATE api_keys SET last_used_at = now() WHERE id = $1;

-- name: ListAPIKeysByOrg :many
SELECT * FROM api_keys WHERE org_id = $1 ORDER BY created_at DESC;

-- name: DeleteAPIKey :execrows
DELETE FROM api_keys WHERE id = $1 AND org_id = $2;
