-- +goose Up
-- dsn_id is the numeric project id that goes in a Sentry-style DSN. @sentry/*
-- SDKs validate the DSN's last path segment is numeric and disable their
-- transport ("Invalid projectId") when it is not, so the cuid primary key
-- cannot be used there. Ingest still authenticates on the unguessable
-- public_key; dsn_id is just the routing/display id Sentry SDKs accept.
ALTER TABLE projects ADD COLUMN dsn_id TEXT;

-- Backfill existing rows with a unique numeric id (base 10^9 + ordinal).
WITH numbered AS (
    SELECT id, (1000000000 + row_number() OVER (ORDER BY created_at))::text AS n
    FROM projects
)
UPDATE projects p SET dsn_id = numbered.n
FROM numbered WHERE p.id = numbered.id;

ALTER TABLE projects ALTER COLUMN dsn_id SET NOT NULL;
ALTER TABLE projects ADD CONSTRAINT projects_dsn_id_key UNIQUE (dsn_id);
CREATE INDEX idx_projects_dsn_id ON projects (dsn_id);

-- +goose Down
ALTER TABLE projects DROP COLUMN IF EXISTS dsn_id;
