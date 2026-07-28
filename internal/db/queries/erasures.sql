-- ciguard:allow-unscoped pending_erasures records an obligation that OUTLIVES the tenant it names; for an org deletion the org row is already gone, so there is no org to scope to and scoping would hide exactly the rows that matter
-- name: RecordPendingErasure :exec
INSERT INTO pending_erasures (id, org_id, project_id, scope_column, scope_value, requested_by, cold_tier, reason)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- ciguard:allow-unscoped operator-facing view of outstanding erasure obligations across every org, including deleted ones; scoping it to a live tenant would omit the deleted-org case this table exists for
-- name: ListOpenErasures :many
SELECT * FROM pending_erasures WHERE completed_at IS NULL ORDER BY requested_at;

-- ciguard:allow-unscoped same as ListOpenErasures: a count across all orgs, including deleted ones
-- name: CountOpenErasures :one
SELECT count(*) FROM pending_erasures WHERE completed_at IS NULL;
