-- +goose Up
-- Route a notification channel to specific projects.
--
-- Delivery was org-wide: every enabled channel received every alert from every
-- project, so a production incident and a side project shared one Slack. The
-- only way to stop a channel receiving something was to delete it, which also
-- destroyed a config you cannot read back.
--
-- EMPTY MEANS ALL PROJECTS. A channel with no rows here behaves exactly as it
-- does today, so this migration changes no existing behaviour, needs no backfill
-- and has no flag day. Routing is opt-in per channel.
--
-- A join table rather than a nullable project_id column on the channel: a
-- channel typically serves two or three related projects, and a single column
-- would force the same webhook config to be duplicated per project, which is the
-- part people already dislike about org-wide delivery.
--
-- Per-RULE routing is deliberately not modelled yet. Rules are already
-- per-project, so per-rule targeting only matters when you want different
-- channels for different rule TYPES inside one project (spikes to Slack, new
-- issues to email). That is real but rarer, and it extends this table later with
-- a nullable rule_type column without changing what is here.
CREATE TABLE IF NOT EXISTS channel_projects (
    channel_id TEXT NOT NULL REFERENCES notification_channels (id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    PRIMARY KEY (channel_id, project_id)
);

-- The dispatch lookup is per project on the alert path, so index that direction.
CREATE INDEX IF NOT EXISTS channel_projects_project_idx ON channel_projects (project_id);

-- +goose Down
DROP TABLE IF EXISTS channel_projects;
