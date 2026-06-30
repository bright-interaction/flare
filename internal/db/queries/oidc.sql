-- name: UpsertOIDCConfig :exec
INSERT INTO oidc_config (org_id, issuer, client_id, client_secret, default_role, enabled)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (org_id) DO UPDATE
  SET issuer = EXCLUDED.issuer, client_id = EXCLUDED.client_id,
      client_secret = EXCLUDED.client_secret, default_role = EXCLUDED.default_role,
      enabled = EXCLUDED.enabled;

-- name: GetOIDCConfig :one
SELECT * FROM oidc_config WHERE org_id = $1;

-- name: DeleteOIDCConfig :exec
DELETE FROM oidc_config WHERE org_id = $1;
