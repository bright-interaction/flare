# Flare

Sovereign observability: errors, logs, and traces on one Postgres you control.

Self-hostable single binary (Go + embedded SvelteKit). No ClickHouse, no heavy
daemon. Storage scales up a ladder: time-partitioned Postgres (hot) -> embedded
DuckDB columnar queries -> Parquet on object storage (cold), all behind one
pluggable `Store` interface.

## Status

- **Phase 1 (done):** service skeleton, control plane (orgs, users, projects +
  DSN, API keys), partitioned telemetry schema, auth (sessions + API keys),
  tenant-scoping ciguard, SvelteKit shell, Docker + Trigger deploy workflow.
- **Phase 2-4 (next):** errors, logs, traces pillars + ingest (OTLP +
  Sentry-envelope) + dashboards.
- **Phase 5:** Cloud (Dockyard) surface: per-service DSN auto-injection,
  Observability tab, alert routing.

## Architecture

- `cmd/server` - entrypoint; embeds the SvelteKit build via `go:embed`.
- `internal/config` - env config; production refuses to boot without secrets.
- `internal/db` - pgx pool, goose migrations (incl. partitioned telemetry
  tables), sqlc-generated queries.
- `internal/auth` - sessions (scs + pgxstore), bcrypt passwords, API keys.
- `internal/api` - chi router, handlers, CSRF, SPA fallback.
- `internal/ciguard` - source-parsing CI guard: every query reading a
  tenant-scoped table must filter on `org_id` (or carry an explicit
  `-- ciguard:allow-unscoped` marker).
- `frontend` - SvelteKit (Svelte 5 + Tailwind 4), built with Bun.

## Local development

```bash
# 1. Postgres
docker run -d --name flare-pg -e POSTGRES_PASSWORD=flare -e POSTGRES_DB=flare \
  -p 55432:5432 postgres:16-alpine

# 2. Backend (applies migrations on boot)
DATABASE_URL='postgres://postgres:flare@localhost:55432/flare?sslmode=disable' \
  PORT=8095 DISABLE_CSRF=true ENVIRONMENT=development \
  BASE_URL='http://localhost:8095' go run ./cmd/server

# 3. Frontend (hot reload, proxies /api to :8095)
cd frontend && bun install && bun run dev
```

`DISABLE_CSRF=true` is honored only when `ENVIRONMENT != production`.

## Build

```bash
cd frontend && bun install && bun run build
cp -r frontend/build cmd/server/frontend/build   # what go:embed serves
go build ./cmd/server
# or the whole container:
docker build -t flare .
```

## Deploy

Push to `flare/**`; the Trigger (Hephaestus) workflow `deploy-flare` rsyncs to
`/opt/flare-build/flare` and runs docker compose. Use `git psync`, not bare
`git push`. The deploy generates `.env` secrets on first run and preserves them
after.
