-- +goose Up
-- Reject non-finite metric values at the STORAGE layer, not only at ingest.
--
-- handleIngestMetrics already drops NaN and ±Infinity (metrics_handlers.go:75),
-- and that is the right place for the friendly rejection. But it is the only
-- thing standing between a bad float and the table, and the consequence of one
-- getting through is out of proportion to the effort of a CHECK: a single NaN
-- row poisons min()/max()/avg() for that metric forever, and Go's encoding/json
-- cannot marshal NaN or Inf, so the metrics query API returns a 500 on every
-- request touching that series. The row is invisible in the UI while breaking
-- the endpoint that would show it.
--
-- A constraint also means the guarantee survives a second write path. Today the
-- handler is the only inserter; an OTLP metrics ingest or a backfill script
-- would not automatically inherit that check, and would not know it needed to.
--
-- The NaN test is "value <> 'NaN'", NOT the usual "value = value".
--
-- In IEEE 754, NaN is the only value not equal to itself, so "value = value" is
-- the standard portable NaN check. Postgres deliberately does NOT follow IEEE
-- here: it treats NaN as equal to itself and greater than all other values, so
-- that floats can be sorted and used in btree indexes. "value = value" is
-- therefore TRUE for NaN and the constraint is a silent no-op.
--
-- I wrote it the IEEE way first and it accepted a NaN row on the live schema
-- ("INSERT 0 1"). The form below was verified against that same schema: NaN,
-- Infinity and -Infinity are all rejected, and 42.5 inserts.
--
-- Added NOT VALID on purpose. metrics is a PARTITIONED parent, so a plain
-- ADD CONSTRAINT would take AccessExclusive on the parent AND every child and
-- scan every existing row before it commits. This migration runs in-process at
-- server boot, so that scan is a metric-ingest outage for its whole duration,
-- and a single pre-existing non-finite row (say from a second write path that
-- did not go through handleIngestMetrics) would ABORT the ADD CONSTRAINT and
-- roll back the entire goose deploy. NOT VALID is metadata-only: it takes a
-- brief lock, does NOT scan, and cannot fail on existing rows, yet Postgres
-- still enforces the CHECK on every INSERT/UPDATE from this point on, so the
-- guarantee for new writes is immediate. The existing rows are verified
-- separately by migration 029 (VALIDATE CONSTRAINT), which scans under
-- ShareUpdateExclusive and does not block ingest.
--
-- Do NOT change the CHECK expression below. The '<>' form is deliberate (see
-- the NaN note above); the only thing this edit changes is adding NOT VALID.
ALTER TABLE metrics
    ADD CONSTRAINT metrics_value_finite
    CHECK (
        value <> 'NaN'::double precision
        AND value <> 'Infinity'::double precision
        AND value <> '-Infinity'::double precision
    ) NOT VALID;

-- +goose Down
ALTER TABLE metrics DROP CONSTRAINT IF EXISTS metrics_value_finite;
