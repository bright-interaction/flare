package analytics

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// HourBucket is one (hour, severity) log-volume cell.
type HourBucket struct {
	Hour     time.Time `json:"hour"`
	Severity string    `json:"severity"`
	Count    int64     `json:"count"`
}

// SpanLatency is per-operation latency percentiles over the window.
type SpanLatency struct {
	Name  string  `json:"name"`
	P50   float64 `json:"p50_ms"`
	P95   float64 `json:"p95_ms"`
	P99   float64 `json:"p99_ms"`
	Count int64   `json:"count"`
}

// source builds a "(hot UNION ALL cold) AS src" subquery exposing exactly the
// requested columns. The cold (Parquet) arm is included only when cold data is
// known to exist, because DuckDB errors on a read_parquet glob that matches
// nothing. Tenant scoping is applied by the OUTER query against src, covering
// both arms.
func (m *Manager) source(table string, cols []string) string {
	colList := strings.Join(cols, ", ")
	hot := fmt.Sprintf("SELECT %s FROM hot.public.%s", colList, table)
	if !m.coldAvailable(table) {
		return "(" + hot + ") AS src"
	}
	glob := m.cfg.parquetRoot() + "/" + table + "/dt=*/part.parquet"
	cold := fmt.Sprintf("SELECT %s FROM read_parquet('%s', union_by_name => true)", colList, escapeSQL(glob))
	return "(" + hot + " UNION ALL " + cold + ") AS src"
}

// coldAvailable reports whether any exported Parquet exists for the table.
func (m *Manager) coldAvailable(table string) bool {
	if m.cfg.S3Endpoint == "" {
		matches, _ := filepath.Glob(filepath.Join(m.cfg.ParquetDir, table, "dt=*", "part.parquet"))
		return len(matches) > 0
	}
	// S3: gate on the export_log marker (best-effort; missing table => no cold).
	// row_count > 0 is required: an empty aged partition writes a row_count=0
	// marker WITHOUT producing any Parquet file, so counting markers alone made
	// this claim cold data existed and every analytics query then failed on a
	// read_parquet glob that matches nothing.
	var n int
	if err := m.db.QueryRow(`SELECT count(*) FROM hot.public.export_log WHERE tbl = ? AND row_count > 0`, table).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// LogVolumeByHour returns hourly log counts by severity for a project over the
// last sinceHours hours, querying hot Postgres unioned with cold Parquet.
func (m *Manager) LogVolumeByHour(ctx context.Context, projectID, orgID string, sinceHours int) ([]HourBucket, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return nil, fmt.Errorf("analytics: closed")
	}
	src := m.source("logs", []string{"observed_at", "severity", "project_id", "org_id"})
	q := fmt.Sprintf(`
		SELECT date_trunc('hour', observed_at) AS hour, severity, count(*) AS cnt
		FROM %s
		WHERE project_id = ? AND org_id = ? AND observed_at >= now() - (? * INTERVAL '1 hour')
		GROUP BY 1, 2
		ORDER BY 1`, src)
	rows, err := m.db.QueryContext(ctx, q, projectID, orgID, sinceHours)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HourBucket{}
	for rows.Next() {
		var b HourBucket
		if err := rows.Scan(&b.Hour, &b.Severity, &b.Count); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// SpanLatencyPercentiles returns p50/p95/p99 duration per span name for a
// project over the last sinceHours hours. This is the kind of GROUP BY +
// approx_quantile aggregation DuckDB does far faster than the PG planner.
func (m *Manager) SpanLatencyPercentiles(ctx context.Context, projectID, orgID string, sinceHours int) ([]SpanLatency, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return nil, fmt.Errorf("analytics: closed")
	}
	src := m.source("spans", []string{"name", "duration_ms", "project_id", "org_id", "start_time"})
	q := fmt.Sprintf(`
		SELECT name,
		       approx_quantile(duration_ms, 0.50) AS p50,
		       approx_quantile(duration_ms, 0.95) AS p95,
		       approx_quantile(duration_ms, 0.99) AS p99,
		       count(*) AS cnt
		FROM %s
		WHERE project_id = ? AND org_id = ? AND start_time >= now() - (? * INTERVAL '1 hour')
		GROUP BY 1
		ORDER BY p95 DESC
		LIMIT 50`, src)
	rows, err := m.db.QueryContext(ctx, q, projectID, orgID, sinceHours)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SpanLatency{}
	for rows.Next() {
		var s SpanLatency
		if err := rows.Scan(&s.Name, &s.P50, &s.P95, &s.P99, &s.Count); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
