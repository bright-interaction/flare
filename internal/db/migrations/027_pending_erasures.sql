-- +goose Up
-- A durable record of an erasure that was only partly fulfilled.
--
-- PurgeColdScope refuses on an S3/MinIO cold tier (it has no object rewrite), so
-- a right-to-erasure request erases the hot tier and leaves aged telemetry in
-- the bucket. The API said so in the response body, which nothing read, and then
-- the obligation existed only in a log line.
--
-- For an ORG deletion that is worse than it sounds: the org row is gone, so
-- there is no tenant left to notice, no audit row that outlives it, and nobody
-- to ask. The request was made, it was not fulfilled, and the only trace was a
-- line in a log that rotates.
--
-- Deliberately NO foreign keys. This table exists precisely for the case where
-- the org and project rows have been deleted, so referencing them would delete
-- the evidence at exactly the moment it matters. The ids are recorded as plain
-- text for the operator to act on.
CREATE TABLE IF NOT EXISTS pending_erasures (
    id            TEXT PRIMARY KEY,
    org_id        TEXT        NOT NULL,
    project_id    TEXT,
    scope_column  TEXT        NOT NULL,
    scope_value   TEXT        NOT NULL,
    requested_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    requested_by  TEXT,
    cold_tier     TEXT        NOT NULL,
    reason        TEXT        NOT NULL,
    completed_at  TIMESTAMPTZ,
    completed_by  TEXT
);

CREATE INDEX IF NOT EXISTS pending_erasures_open_idx
    ON pending_erasures (requested_at)
    WHERE completed_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS pending_erasures;
