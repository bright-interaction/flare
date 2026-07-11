-- +goose Up
-- Metrics pillar: numeric time series (gauges, counters/sums, histogram
-- summaries) so the estate's operational questions - request rate, p95 latency,
-- queue depth, disk %, goroutine count - land in Flare instead of needing a
-- separate Prometheus/Grafana. Time-partitioned like events/logs/spans, so the
-- same retention (DROP PARTITION) + cold-tier export apply.
CREATE TABLE metrics (
    id          TEXT NOT NULL,
    project_id  TEXT NOT NULL,
    org_id      TEXT NOT NULL,
    name        TEXT NOT NULL,
    kind        TEXT NOT NULL DEFAULT 'gauge',   -- gauge | sum | histogram
    value       DOUBLE PRECISION NOT NULL DEFAULT 0,
    labels      JSONB,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, observed_at)
) PARTITION BY RANGE (observed_at);
CREATE TABLE metrics_default PARTITION OF metrics DEFAULT;
CREATE INDEX idx_metrics_name ON metrics (project_id, name, observed_at DESC);
CREATE INDEX idx_metrics_observed_brin ON metrics USING BRIN (observed_at);

-- +goose Down
DROP TABLE IF EXISTS metrics;
