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

-- name: ListOpenErasuresByOrg :many
-- Org-scoped view of outstanding obligations for the admin endpoint. Unlike
-- ListOpenErasures (operator-facing, every org) this is filtered to the caller's
-- org so an admin never sees another tenant's obligations. It cannot surface a
-- deleted org's own obligation (there is no org left to authenticate as); that
-- case stays with the operator logs, which is why LogOpenErasures spans all orgs.
SELECT * FROM pending_erasures
WHERE completed_at IS NULL AND org_id = $1
ORDER BY requested_at;

-- name: CompletePendingErasure :execrows
-- Marks one obligation fulfilled. Scoped by org_id as well as id so an admin can
-- only ever complete their OWN org's obligation, never another tenant's by id.
-- Idempotent guard on completed_at: a second call returns 0 rows rather than
-- overwriting the original actor/time. Returns the affected row count so the
-- handler can 404 a wrong id/org instead of reporting a phantom success.
UPDATE pending_erasures
SET completed_at = now(), completed_by = $3
WHERE id = $1 AND org_id = $2 AND completed_at IS NULL;
