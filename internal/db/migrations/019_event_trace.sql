-- +goose Up
-- Join key from an error to the distributed trace it occurred in. Sentry SDKs
-- put this on every event as contexts.trace.trace_id/span_id, but Flare was
-- dropping it, so an exception could never be correlated to its trace or the
-- logs around it (logs and spans already carry trace_id). Nullable: bare
-- messages and non-traced code paths carry no trace.
ALTER TABLE events ADD COLUMN trace_id text;
ALTER TABLE events ADD COLUMN span_id text;

-- +goose Down
ALTER TABLE events DROP COLUMN trace_id;
ALTER TABLE events DROP COLUMN span_id;
