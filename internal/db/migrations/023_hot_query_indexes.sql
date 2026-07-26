-- +goose Up
-- Indexes for the hot read shapes of the logs and spans pillars.
--
-- Both tables only had a BRIN index on their time column plus a trace_id index.
-- BRIN prunes by physical block range, which does nothing for the actual query
-- shape: "this project's rows, newest first". So every logs search and every
-- trace list scanned all rows in every retained partition and sorted them.
-- These are declared on the partitioned parents, so Postgres creates and
-- maintains the matching index on every existing and future child partition.
CREATE INDEX IF NOT EXISTS idx_logs_project_observed
    ON logs (project_id, org_id, observed_at DESC);

CREATE INDEX IF NOT EXISTS idx_spans_project_start
    ON spans (project_id, org_id, start_time DESC);

-- GetTraceSpans filters trace_id + project_id + org_id; the bare trace_id index
-- forces a filter step over every project sharing a trace id.
CREATE INDEX IF NOT EXISTS idx_spans_trace_project
    ON spans (trace_id, project_id, org_id);

-- ListIssues orders by last_seen within a project AND filters on status; the
-- existing (project_id, last_seen) index cannot serve the filtered tabs.
CREATE INDEX IF NOT EXISTS idx_issues_project_status_lastseen
    ON issues (project_id, org_id, status, last_seen DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_issues_project_status_lastseen;
DROP INDEX IF EXISTS idx_spans_trace_project;
DROP INDEX IF EXISTS idx_spans_project_start;
DROP INDEX IF EXISTS idx_logs_project_observed;
