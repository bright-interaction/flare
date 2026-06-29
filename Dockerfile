# Flare observability engine (self-contained build).
#
# Local build:
#   cd flare && docker build -t flare .
#
# Pure-Go for now. The DuckDB columnar analytics tier (logs/traces phases)
# will switch the backend stage to the glibc/bookworm toolchain, matching
# atomicsite's analyticsdb build.

# Stage 1: SvelteKit dashboard.
FROM oven/bun:1-alpine AS frontend
WORKDIR /app/frontend
COPY frontend/package.json frontend/bun.lock ./
RUN --mount=type=cache,target=/root/.bun/install/cache \
    bun install --frozen-lockfile
COPY frontend/ .
RUN bun run build

# Stage 2: Go server. The frontend build is copied in for go:embed.
FROM golang:1.26-alpine AS backend
WORKDIR /app
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
COPY --from=frontend /app/frontend/build ./cmd/server/frontend/build
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /flare ./cmd/server

# Stage 3: runtime.
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata wget \
    && adduser -D -u 1000 flare
COPY --from=backend /flare /usr/local/bin/flare
USER flare
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/api/health || exit 1
ENTRYPOINT ["/usr/local/bin/flare"]
