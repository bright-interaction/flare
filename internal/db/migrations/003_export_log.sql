-- +goose Up
-- Ledger of telemetry partitions exported to the Parquet cold tier. Operational
-- metadata (not tenant data), so it carries no org_id and is intentionally
-- outside the ciguard tenant-scoping set. The retention step gates DROP on a
-- successful row here, so a day is dropped from Postgres only after it is
-- durably in Parquet.
CREATE TABLE export_log (
    tbl         TEXT NOT NULL,
    day         DATE NOT NULL,
    exported_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    row_count   BIGINT NOT NULL,
    PRIMARY KEY (tbl, day)
);

-- +goose Down
DROP TABLE IF EXISTS export_log;
