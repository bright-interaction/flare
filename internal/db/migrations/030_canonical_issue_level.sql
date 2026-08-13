-- +goose Up
-- Fold existing issue levels to the one spelling every query compares against.
--
-- ingest.canonicalLevel now lowercases and trims at the trust boundary, so no
-- new row can arrive as "INFO". This handles the rows that already did.
--
-- Why issues and not events. Ten SQL predicates spell
-- `AND level NOT IN ('info', 'debug')` with raw case-sensitive equality; six of
-- them read `issues` and four read `events`. issues holds one row per
-- fingerprint, so this UPDATE is small and touches only the rows that actually
-- differ. The four `events` predicates are all time-bounded
-- (OverviewEventCount24h, the two volume-by-hour queries, and the watchdog's
-- CountActionableEventsForProjectSince), so within one window of deploying the
-- boundary fix they are reading only canonical rows and need no backfill.
--
-- That distinction is the point, not a shortcut: `events` is a partitioned
-- parent, and an unbounded UPDATE across every child at boot is precisely the
-- ingest-blocking DDL shape internal/ciguard/guard_partition_constraint_test.go
-- exists to refuse.
--
-- The WHERE clause makes this a no-op on an already-canonical database, so it
-- costs one index-free scan of a small table once and nothing thereafter.
UPDATE issues SET level = lower(btrim(level)) WHERE level <> lower(btrim(level));

-- +goose Down
-- Not reversible, and there is nothing to reverse to: the original casing was
-- never meaningful, it was a bug that made one value read as two.
SELECT 1;
