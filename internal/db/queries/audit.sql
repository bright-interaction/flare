-- name: InsertAuditLog :exec
INSERT INTO audit_log (id, org_id, actor_user_id, action, target)
VALUES ($1, $2, $3, $4, $5);

-- name: ListAuditLog :many
-- Newest first, joined to the actor's email (NULL = API key or removed user).
SELECT a.action, a.target, a.created_at, u.email AS actor_email
FROM audit_log a
LEFT JOIN users u ON u.id = a.actor_user_id
WHERE a.org_id = $1
ORDER BY a.created_at DESC
LIMIT 200;
