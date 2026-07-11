-- +goose Up
-- Cron / scheduled-job check-in monitors. A job pings a check-in URL each run;
-- if a ping is overdue (interval + grace elapsed) the watchdog fires a "missing"
-- alert, and a ping that reports failure fires immediately. This catches a
-- silently-dead scheduled job, which neither service-liveness heartbeats nor
-- error alerts can see: a cron that simply stops running emits nothing at all.
CREATE TABLE monitors (
    id               TEXT PRIMARY KEY,
    org_id           TEXT NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    project_id       TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    slug             TEXT NOT NULL,
    name             TEXT NOT NULL DEFAULT '',
    interval_seconds INTEGER NOT NULL DEFAULT 0,   -- 0 = unconfigured; missing-detection off until set
    grace_seconds    INTEGER NOT NULL DEFAULT 300,
    last_ping_at     TIMESTAMPTZ,
    last_status      TEXT NOT NULL DEFAULT '',      -- ok | error, as reported by the last check-in
    state            TEXT NOT NULL DEFAULT 'new',    -- new | ok | missing | failed
    last_alert_at    TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, slug)
);
CREATE INDEX idx_monitors_project ON monitors (project_id);

-- +goose Down
DROP TABLE IF EXISTS monitors;
