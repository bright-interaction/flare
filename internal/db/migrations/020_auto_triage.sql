-- +goose Up
-- Auto-triage: run AI triage automatically the first time an issue is seen,
-- gated by a per-org toggle and a daily budget. The budget is the load-bearing
-- guardrail: without it, turning triage on-ingest would run one BYOAI
-- completion per new fingerprint, so an error storm becomes a spend storm.
ALTER TABLE ai_config ADD COLUMN auto_triage BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE ai_config ADD COLUMN triage_daily_budget INTEGER NOT NULL DEFAULT 50;

-- Per-org daily counter the auto-triage path atomically claims against.
CREATE TABLE ai_triage_usage (
    org_id       TEXT NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    day          DATE NOT NULL,
    triage_count INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (org_id, day)
);

-- +goose Down
DROP TABLE IF EXISTS ai_triage_usage;
ALTER TABLE ai_config DROP COLUMN auto_triage;
ALTER TABLE ai_config DROP COLUMN triage_daily_budget;
