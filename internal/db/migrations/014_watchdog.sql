-- +goose Up
-- Watchdog alert rule types: "anomaly" (error rate deviates from the project's
-- own recent baseline) and "silence" (a normally-active project goes quiet).
-- Both are evaluated by a periodic background worker, not on ingest, because
-- silence is the ABSENCE of events. They reuse the existing alert_rules
-- threshold/window_minutes columns; last_fired_at is the per-rule cooldown so a
-- persisting condition pages once, not every tick.
ALTER TABLE alert_rules ADD COLUMN last_fired_at TIMESTAMPTZ;

-- The watchdog runs two per-project event counts every tick. events has only a
-- BRIN on received_at, so a per-project count over 24h scans every tenant's
-- last-24h events and filters project_id in memory. This btree lets the counts
-- prune by project. On the partitioned parent it cascades to all partitions.
CREATE INDEX IF NOT EXISTS idx_events_project_received ON events (project_id, received_at);

-- +goose Down
DROP INDEX IF EXISTS idx_events_project_received;
ALTER TABLE alert_rules DROP COLUMN last_fired_at;
