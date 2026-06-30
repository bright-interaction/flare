-- +goose Up
-- Richer alert rules. window_minutes scopes a spike rule's event count to a
-- rolling window; last_spike_at gives per-issue spike-alert dedup (one spike
-- alert per issue per window, set via an atomic test-and-set).
ALTER TABLE alert_rules ADD COLUMN window_minutes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE issues ADD COLUMN last_spike_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE alert_rules DROP COLUMN IF EXISTS window_minutes;
ALTER TABLE issues DROP COLUMN IF EXISTS last_spike_at;
