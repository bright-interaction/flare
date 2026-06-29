// Package analytics is a read-only embedded DuckDB layer for columnar
// aggregations over Flare's telemetry. It ATTACHes the live Postgres
// (read-only, via the postgres extension) as schema "hot" and unions hot
// partitions with cold Parquet (local dir or S3/MinIO), mirroring the
// atomicsite analyticsdb pattern.
//
// Writes never go through here; the hot tier (partitioned Postgres) still
// serves every dashboard read. This is additive columnar headroom. Open is
// non-fatal: if DuckDB fails to load, the server runs without analytics.
package analytics

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// Config is everything the analytics layer needs. PostgresDSN is attached
// read-only; ParquetRoot is where the cold tier lives (a local dir, or an
// s3:// URL when S3Endpoint is set). Secrets are passed in, never logged.
type Config struct {
	PostgresDSN string
	ParquetDir  string // local cold-tier dir (used when S3Endpoint == "")

	S3Endpoint  string // optional; when set, cold tier is s3://S3Bucket/flare
	S3Bucket    string
	S3AccessKey string
	S3SecretKey string
	S3Region    string
	S3UseSSL    bool
}

type Manager struct {
	db  *sql.DB
	cfg Config

	mu     sync.RWMutex
	closed bool
}

// parquetRoot is the base URI the export worker writes to and queries read.
func (c Config) parquetRoot() string {
	if c.S3Endpoint != "" {
		return "s3://" + c.S3Bucket + "/flare"
	}
	return strings.TrimRight(c.ParquetDir, "/")
}

// Open creates the in-memory DuckDB, loads extensions, attaches Postgres
// read-only, and (optionally) registers the S3 secret for MinIO. Errors are
// returned so the caller can log + continue without analytics.
func Open(ctx context.Context, cfg Config) (*Manager, error) {
	if strings.TrimSpace(cfg.PostgresDSN) == "" {
		return nil, fmt.Errorf("analytics: PostgresDSN is required")
	}

	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, fmt.Errorf("analytics: open duckdb: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)

	// postgres_scanner (loaded as "postgres") for the hot ATTACH; httpfs only
	// when an S3 endpoint is configured. parquet is bundled in DuckDB core.
	exts := []string{"postgres"}
	if cfg.S3Endpoint != "" {
		exts = append(exts, "httpfs")
	}
	for _, ext := range exts {
		if _, err := db.ExecContext(ctx, "INSTALL "+ext); err != nil {
			slog.Debug("analytics: install extension (treating as preloaded)", "ext", ext, "err", err)
		}
		if _, err := db.ExecContext(ctx, "LOAD "+ext); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("analytics: load %s: %w", ext, err)
		}
	}

	// READ_ONLY attach: analytics must never write to the ingest DB.
	attach := fmt.Sprintf("ATTACH '%s' AS hot (TYPE postgres, READ_ONLY)", escapeSQL(cfg.PostgresDSN))
	if _, err := db.ExecContext(ctx, attach); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("analytics: attach postgres: %w", err)
	}

	if cfg.S3Endpoint != "" {
		secret := fmt.Sprintf(
			"CREATE OR REPLACE SECRET flare_s3 (TYPE s3, KEY_ID '%s', SECRET '%s', ENDPOINT '%s', URL_STYLE 'path', USE_SSL %t, REGION '%s')",
			escapeSQL(cfg.S3AccessKey), escapeSQL(cfg.S3SecretKey), escapeSQL(cfg.S3Endpoint), cfg.S3UseSSL, escapeSQL(cfg.S3Region),
		)
		if _, err := db.ExecContext(ctx, secret); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("analytics: create s3 secret: %w", err)
		}
	}

	slog.Info("analytics: ready", "cold_tier", cfg.parquetRoot(), "s3", cfg.S3Endpoint != "")
	return &Manager{db: db, cfg: cfg}, nil
}

func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	if _, err := m.db.Exec("DETACH hot"); err != nil {
		slog.Debug("analytics: detach hot", "err", err)
	}
	return m.db.Close()
}

// Healthy returns true when DuckDB can read the attached Postgres.
func (m *Manager) Healthy(ctx context.Context) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return false
	}
	row := m.db.QueryRowContext(ctx, "SELECT 1 FROM hot.public.projects LIMIT 1")
	var n int
	if err := row.Scan(&n); err != nil && err != sql.ErrNoRows {
		slog.Warn("analytics: healthcheck failed", "err", err)
		return false
	}
	return true
}

// escapeSQL escapes single quotes for embedding in a DuckDB string literal.
func escapeSQL(s string) string { return strings.ReplaceAll(s, "'", "''") }
