-- +goose NO TRANSACTION
-- +goose Up
-- Index for the issues list, which orders by last_seen within a project AND
-- filters on status; the existing (project_id, last_seen) index cannot serve
-- the filtered tabs.
--
-- CONCURRENTLY, and NO TRANSACTION because CONCURRENTLY cannot run inside a
-- transaction block. goose wraps a migration in one by default, and migrations
-- run in-process at server start, so a plain CREATE INDEX here would hold its
-- lock for the whole build while the previous pod is still serving.
--
-- `issues` is a plain table, so CONCURRENTLY is available. The logs and spans
-- indexes are deliberately NOT here: they are partitioned parents, PostgreSQL
-- rejects CREATE INDEX CONCURRENTLY on a partitioned table, and the plain form
-- takes ShareLock on the parent AND every child for the duration, which would
-- block all ingest. Those need the per-partition ATTACH dance, run as a
-- deliberate maintenance operation rather than silently at boot. Tracked as a
-- follow-up; the queries are correct without them, just slower.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_issues_project_status_lastseen
    ON issues (project_id, org_id, status, last_seen DESC);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_issues_project_status_lastseen;
