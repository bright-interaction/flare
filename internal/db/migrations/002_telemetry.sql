-- +goose Up

-- Grouped errors. One issue per (project, fingerprint); events roll up into it.
CREATE TABLE issues (
    id          TEXT NOT NULL,
    project_id  TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    org_id      TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    title       TEXT NOT NULL,
    culprit     TEXT NOT NULL DEFAULT '',
    level       TEXT NOT NULL DEFAULT 'error',
    status      TEXT NOT NULL DEFAULT 'unresolved',
    platform    TEXT NOT NULL DEFAULT '',
    first_seen  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen   TIMESTAMPTZ NOT NULL DEFAULT now(),
    event_count BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (id),
    UNIQUE (project_id, fingerprint)
);
CREATE INDEX idx_issues_project_lastseen ON issues (project_id, last_seen DESC);
CREATE INDEX idx_issues_org ON issues (org_id);

-- Hot tier: time-partitioned error events. Retention = DROP PARTITION.
-- A DEFAULT partition guarantees inserts never fail before the daily
-- partition-rollover worker has created the dated child (added later).
CREATE TABLE events (
    id              TEXT NOT NULL,
    project_id      TEXT NOT NULL,
    org_id          TEXT NOT NULL,
    issue_id        TEXT,
    level           TEXT NOT NULL DEFAULT 'error',
    message         TEXT NOT NULL DEFAULT '',
    exception_type  TEXT NOT NULL DEFAULT '',
    exception_value TEXT NOT NULL DEFAULT '',
    platform        TEXT NOT NULL DEFAULT '',
    environment     TEXT NOT NULL DEFAULT '',
    release         TEXT NOT NULL DEFAULT '',
    stacktrace      JSONB,
    payload         JSONB,
    received_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, received_at)
) PARTITION BY RANGE (received_at);
CREATE TABLE events_default PARTITION OF events DEFAULT;
CREATE INDEX idx_events_issue ON events (issue_id, received_at DESC);
CREATE INDEX idx_events_received_brin ON events USING BRIN (received_at);

-- Hot tier: time-partitioned structured logs.
CREATE TABLE logs (
    id          TEXT NOT NULL,
    project_id  TEXT NOT NULL,
    org_id      TEXT NOT NULL,
    severity    TEXT NOT NULL DEFAULT 'info',
    body        TEXT NOT NULL DEFAULT '',
    attributes  JSONB,
    trace_id    TEXT NOT NULL DEFAULT '',
    span_id     TEXT NOT NULL DEFAULT '',
    observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, observed_at)
) PARTITION BY RANGE (observed_at);
CREATE TABLE logs_default PARTITION OF logs DEFAULT;
CREATE INDEX idx_logs_observed_brin ON logs USING BRIN (observed_at);
CREATE INDEX idx_logs_trace ON logs (trace_id);

-- Hot tier: time-partitioned trace spans.
CREATE TABLE spans (
    trace_id       TEXT NOT NULL,
    span_id        TEXT NOT NULL,
    parent_span_id TEXT NOT NULL DEFAULT '',
    project_id     TEXT NOT NULL,
    org_id         TEXT NOT NULL,
    name           TEXT NOT NULL DEFAULT '',
    kind           TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL DEFAULT '',
    start_time     TIMESTAMPTZ NOT NULL,
    end_time       TIMESTAMPTZ NOT NULL,
    duration_ms    DOUBLE PRECISION NOT NULL DEFAULT 0,
    attributes     JSONB,
    PRIMARY KEY (trace_id, span_id, start_time)
) PARTITION BY RANGE (start_time);
CREATE TABLE spans_default PARTITION OF spans DEFAULT;
CREATE INDEX idx_spans_trace ON spans (trace_id);
CREATE INDEX idx_spans_start_brin ON spans USING BRIN (start_time);

CREATE TABLE alert_rules (
    id         TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    org_id     TEXT NOT NULL,
    name       TEXT NOT NULL,
    type       TEXT NOT NULL DEFAULT 'new_issue',
    threshold  INTEGER NOT NULL DEFAULT 0,
    enabled    BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_alert_rules_project ON alert_rules (project_id);

CREATE TABLE notification_channels (
    id         TEXT PRIMARY KEY,
    org_id     TEXT NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    type       TEXT NOT NULL,
    config     JSONB NOT NULL,
    enabled    BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_notif_channels_org ON notification_channels (org_id);

-- +goose Down
DROP TABLE IF EXISTS notification_channels;
DROP TABLE IF EXISTS alert_rules;
DROP TABLE IF EXISTS spans;
DROP TABLE IF EXISTS logs;
DROP TABLE IF EXISTS events;
DROP TABLE IF EXISTS issues;
