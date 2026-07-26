-- name: UpsertInvitation :one
INSERT INTO invitations (id, org_id, email, role, token_hash, invited_by, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (org_id, email) DO UPDATE
  SET role = EXCLUDED.role, token_hash = EXCLUDED.token_hash,
      invited_by = EXCLUDED.invited_by, expires_at = EXCLUDED.expires_at,
      accepted_at = NULL, created_at = now()
RETURNING *;

-- ciguard:allow-unscoped accept flow resolves the org/role from the secret token_hash
-- name: GetInvitationByToken :one
SELECT * FROM invitations
WHERE token_hash = $1 AND accepted_at IS NULL AND expires_at > now();

-- name: ListInvitationsByOrg :many
SELECT id, email, role, expires_at, created_at FROM invitations
WHERE org_id = $1 AND accepted_at IS NULL
ORDER BY created_at DESC;

-- name: DeleteInvitation :execrows
DELETE FROM invitations WHERE id = $1 AND org_id = $2;

-- ciguard:allow-unscoped accept flow resolves the invitation from the secret token_hash; the id here is already that resolved row
-- name: MarkInvitationAccepted :exec
UPDATE invitations SET accepted_at = now() WHERE id = $1;
