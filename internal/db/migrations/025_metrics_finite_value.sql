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
-- Safe to add outright: the table is empty and partitioned, so this is instant
-- and cannot fail on existing rows.
ALTER TABLE metrics
    ADD CONSTRAINT metrics_value_finite
    CHECK (
        value <> 'NaN'::double precision
        AND value <> 'Infinity'::double precision
        AND value <> '-Infinity'::double precision
    );

-- +goose Down
ALTER TABLE metrics DROP CONSTRAINT IF EXISTS metrics_value_finite;
