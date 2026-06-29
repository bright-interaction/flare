// Package partition keeps the time-partitioned telemetry tables healthy: it
// pre-creates daily range partitions ahead of ingest and drops partitions
// past the retention window (retention = DROP PARTITION, instant, no VACUUM
// bloat). The DEFAULT partition created in the migration is the safety net for
// out-of-range timestamps; in normal operation rows land in dated partitions.
package partition

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var tables = []string{"events", "logs", "spans"}

// Exporter archives a dated partition to the cold tier before it is dropped.
// nil = no cold tier (just drop). analytics.Exporter satisfies this.
type Exporter interface {
	ExportDay(ctx context.Context, table string, day time.Time) error
}

type Manager struct {
	pool          *pgxpool.Pool
	retentionDays int
	aheadDays     int
	exporter      Exporter
}

func New(pool *pgxpool.Pool, retentionDays int, exporter Exporter) *Manager {
	if retentionDays <= 0 {
		retentionDays = 30
	}
	return &Manager{pool: pool, retentionDays: retentionDays, aheadDays: 3, exporter: exporter}
}

// Run does one pass now, then every 6 hours until ctx is cancelled.
func (m *Manager) Run(ctx context.Context) {
	m.once(ctx)
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.once(ctx)
		}
	}
}

func (m *Manager) once(ctx context.Context) {
	for _, tbl := range tables {
		for d := 0; d <= m.aheadDays; d++ {
			day := time.Now().UTC().AddDate(0, 0, d)
			if err := ensurePartition(ctx, m.pool, tbl, day); err != nil {
				slog.Warn("partition ensure failed", "table", tbl, "day", day.Format("2006-01-02"), "error", err)
			}
		}
		if err := m.prune(ctx, tbl); err != nil {
			slog.Warn("partition prune failed", "table", tbl, "error", err)
		}
	}
}

// ensurePartition creates the dated child partition if absent. Table names come
// from the fixed `tables` allowlist plus a date, so the formatted SQL is safe.
func ensurePartition(ctx context.Context, pool *pgxpool.Pool, table string, day time.Time) error {
	day = day.UTC()
	name := fmt.Sprintf("%s_%s", table, day.Format("2006_01_02"))
	from := day.Format("2006-01-02") + " 00:00:00+00"
	to := day.AddDate(0, 0, 1).Format("2006-01-02") + " 00:00:00+00"
	q := fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')",
		name, table, from, to,
	)
	_, err := pool.Exec(ctx, q)
	return err
}

func (m *Manager) prune(ctx context.Context, table string) error {
	names, err := childPartitions(ctx, m.pool, table)
	if err != nil {
		return err
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -m.retentionDays).Truncate(24 * time.Hour)
	for _, name := range names {
		suffix := name[len(table)+1:] // YYYY_MM_DD
		d, err := time.Parse("2006_01_02", suffix)
		if err != nil {
			continue
		}
		if d.Before(cutoff) {
			// Export-then-drop: a partition is only dropped after its rows are
			// durably in the Parquet cold tier. If export fails, keep the
			// partition and retry next cycle (no data loss).
			if m.exporter != nil {
				if err := m.exporter.ExportDay(ctx, table, d); err != nil {
					slog.Warn("partition export failed; keeping partition for retry", "partition", name, "error", err)
					continue
				}
			}
			if _, err := m.pool.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", name)); err != nil {
				slog.Warn("drop partition failed", "partition", name, "error", err)
				continue
			}
			slog.Info("dropped expired partition", "partition", name)
		}
	}
	return nil
}

// childPartitions returns the dated child partition names of a parent table.
func childPartitions(ctx context.Context, p *pgxpool.Pool, table string) ([]string, error) {
	rows, err := p.Query(ctx, `
		SELECT c.relname
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class parent ON parent.oid = i.inhparent
		WHERE parent.relname = $1
		  AND c.relname ~ ('^' || $1 || '_[0-9]{4}_[0-9]{2}_[0-9]{2}$')`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}
