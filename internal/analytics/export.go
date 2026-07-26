package analytics

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Exporter archives aged telemetry partitions to the Parquet cold tier. It uses
// the DuckDB manager (read-only Postgres attach) to COPY straight from the live
// partition, and the pgx pool to write the export_log ledger (DuckDB's attach
// is read-only, so the ledger write cannot go through it).
type Exporter struct {
	duck *Manager
	pool *pgxpool.Pool
}

func NewExporter(duck *Manager, pool *pgxpool.Pool) *Exporter {
	return &Exporter{duck: duck, pool: pool}
}

// ExportDay exports the dated child partition (e.g. logs_2026_05_20) of table to
// Parquet, verifies the written row count matches the source, and records it in
// export_log. Idempotent: a (table, day) already logged is skipped. Returns nil
// when there is nothing to export (partition missing or empty). The caller
// (partition retention) must only DROP after this returns nil.
// The whole body runs under holdColdTier (the SHARED analytics lock), so an
// export can never interleave with PurgeColdScope, which erases the cold tier
// for a right-to-erasure request under the exclusive lock. See holdColdTier for
// the interleaving that used to resurrect erased telemetry.
func (e *Exporter) ExportDay(ctx context.Context, table string, day time.Time) error {
	return e.duck.holdColdTier(func() error { return e.exportDay(ctx, table, day) })
}

func (e *Exporter) exportDay(ctx context.Context, table string, day time.Time) error {
	day = day.UTC()
	dayStr := day.Format("2006-01-02")
	child := fmt.Sprintf("%s_%s", table, day.Format("2006_01_02"))

	var existing int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM export_log WHERE tbl=$1 AND day=$2`, table, day).Scan(&existing); err == nil && existing > 0 {
		return nil // already exported
	}

	// Only a genuinely ABSENT partition is "nothing to archive". Deciding that
	// from a failed DuckDB count conflated every transient DuckDB fault (attach
	// dropped, extension missing, disk full, read-only replica lag) with
	// "missing", and the caller drops the partition when this returns nil - so a
	// blip meant permanent, silent telemetry loss. Ask Postgres directly, over
	// the pgx pool, and only then return nil.
	var childExists bool
	if err := e.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = $1 AND relkind = 'r')`, child,
	).Scan(&childExists); err != nil {
		return fmt.Errorf("check partition %s exists: %w", child, err)
	}
	if !childExists {
		return nil // child partition does not exist; nothing to archive
	}

	d := e.duck.db
	var srcCount int64
	if err := d.QueryRowContext(ctx, fmt.Sprintf("SELECT count(*) FROM hot.public.%s", child)).Scan(&srcCount); err != nil {
		return fmt.Errorf("count rows in %s: %w", child, err)
	}
	if srcCount == 0 {
		_, _ = e.pool.Exec(ctx, `INSERT INTO export_log (tbl, day, row_count) VALUES ($1,$2,0) ON CONFLICT (tbl,day) DO NOTHING`, table, day)
		return nil
	}

	root := e.duck.cfg.parquetRoot()
	destDir := root + "/" + table + "/dt=" + dayStr
	final := destDir + "/part.parquet"
	writeTarget := final
	local := e.duck.cfg.S3Endpoint == ""
	if local {
		if err := os.MkdirAll(filepath.FromSlash(destDir), 0o755); err != nil {
			return fmt.Errorf("mkdir parquet dir: %w", err)
		}
		writeTarget = final + ".tmp"
	}

	copySQL := fmt.Sprintf("COPY (SELECT * FROM hot.public.%s) TO '%s' (FORMAT parquet, COMPRESSION zstd)", child, escapeSQL(writeTarget))
	if _, err := d.ExecContext(ctx, copySQL); err != nil {
		return fmt.Errorf("copy %s to parquet: %w", child, err)
	}

	var pqCount int64
	if err := d.QueryRowContext(ctx, fmt.Sprintf("SELECT count(*) FROM read_parquet('%s')", escapeSQL(writeTarget))).Scan(&pqCount); err != nil {
		return fmt.Errorf("verify parquet: %w", err)
	}
	if pqCount != srcCount {
		return fmt.Errorf("parquet row count %d != source %d for %s (not committing)", pqCount, srcCount, child)
	}

	if local {
		if err := os.Rename(filepath.FromSlash(writeTarget), filepath.FromSlash(final)); err != nil {
			return fmt.Errorf("rename parquet: %w", err)
		}
	}

	if _, err := e.pool.Exec(ctx,
		`INSERT INTO export_log (tbl, day, row_count) VALUES ($1,$2,$3)
		 ON CONFLICT (tbl,day) DO UPDATE SET row_count=EXCLUDED.row_count, exported_at=now()`,
		table, day, srcCount); err != nil {
		return fmt.Errorf("write export_log: %w", err)
	}
	return nil
}
