-- +goose Up
-- Marks an issue whose telemetry contained a secret / JWT / payment card
-- (detected on ingest). Comma-separated kinds, e.g. 'secret,card'. Empty = no
-- sensitive data seen. A compliance + leak signal: a value that should never
-- have been logged.
ALTER TABLE issues ADD COLUMN sensitive TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE issues DROP COLUMN sensitive;
