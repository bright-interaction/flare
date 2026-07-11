-- name: CreateAlertRule :one
INSERT INTO alert_rules (id, project_id, org_id, name, type, threshold, window_minutes, enabled)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: ListAlertRulesByProject :many
SELECT * FROM alert_rules WHERE project_id = $1 AND org_id = $2 ORDER BY created_at DESC;

-- name: ListEnabledAlertRulesByProject :many
SELECT * FROM alert_rules WHERE project_id = $1 AND org_id = $2 AND enabled = true;

-- name: CreateNotificationChannel :one
INSERT INTO notification_channels (id, org_id, type, config, enabled)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ListNotificationChannelsByOrg :many
SELECT * FROM notification_channels WHERE org_id = $1 ORDER BY created_at DESC;

-- name: ListEnabledNotificationChannelsByOrg :many
SELECT * FROM notification_channels WHERE org_id = $1 AND enabled = true;

-- name: DeleteAlertRule :execrows
DELETE FROM alert_rules WHERE id = $1 AND project_id = $2 AND org_id = $3;

-- name: DeleteNotificationChannel :execrows
DELETE FROM notification_channels WHERE id = $1 AND org_id = $2;

-- name: GetNotificationChannel :one
SELECT * FROM notification_channels WHERE id = $1 AND org_id = $2;

-- name: CountEnabledNotificationChannelsByOrg :one
SELECT count(*) FROM notification_channels WHERE org_id = $1 AND enabled = true;

-- name: CountEnabledAlertRulesByOrg :one
SELECT count(*) FROM alert_rules WHERE org_id = $1 AND enabled = true;

-- name: RecordChannelDelivery :exec
UPDATE notification_channels
SET last_attempt_at = @attempted_at,
    last_ok_at = CASE WHEN @ok::boolean THEN @attempted_at ELSE last_ok_at END,
    last_error = CASE WHEN @ok::boolean THEN NULL ELSE @error_msg END
WHERE id = @id AND org_id = @org_id;
