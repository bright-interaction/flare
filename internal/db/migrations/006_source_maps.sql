-- +goose Up
-- Source map artifacts for stack-trace symbolication. Each row holds one
-- minified file's source map (the .map JSON), scoped to a project + release.
-- On read, a minified frame is matched to its map by (project, release, name)
-- and resolved back to the original source position + context.
CREATE TABLE source_map_artifacts (
    id         TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    org_id     TEXT NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    release    TEXT NOT NULL,
    name       TEXT NOT NULL,
    content    TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, release, name)
);
CREATE INDEX idx_smap_lookup ON source_map_artifacts (project_id, org_id, release);

-- +goose Down
DROP TABLE IF EXISTS source_map_artifacts;
