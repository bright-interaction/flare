-- +goose Up
-- Release tracking. A release is a version string for a project. Rows are
-- created either by a deploy marker (POST .../releases) or auto-created on the
-- first event that carries that release. first_release on an issue records the
-- version it was first seen in ("introduced in v1.2.3").
CREATE TABLE releases (
    id         TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    org_id     TEXT NOT NULL,
    version    TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, version)
);
CREATE INDEX idx_releases_project ON releases (project_id, org_id);

ALTER TABLE issues ADD COLUMN first_release TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE issues DROP COLUMN IF EXISTS first_release;
DROP TABLE IF EXISTS releases;
