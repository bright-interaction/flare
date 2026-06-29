-- name: CreateAlertRule :one
INSERT INTO alert_rules (id, project_id, org_id, name, type, threshold, enabled)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: ListAlertRulesByProject :many
SELECT * FROM alert_rules WHERE project_id = $1 AND org_id = $2 ORDER BY created_at DESC;

-- name: ListEnabledAlertRulesByProject :many
SELECT * FROM alert_rules WHERE project_id = $1 AND org_id = $2 AND enabled = true AND type = $3;

-- name: CreateNotificationChannel :one
INSERT INTO notification_channels (id, org_id, type, config, enabled)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ListNotificationChannelsByOrg :many
SELECT * FROM notification_channels WHERE org_id = $1 ORDER BY created_at DESC;

-- name: ListEnabledNotificationChannelsByOrg :many
SELECT * FROM notification_channels WHERE org_id = $1 AND enabled = true;
