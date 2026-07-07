package analytics

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestPurgeColdScope(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := &Manager{db: db, cfg: Config{ParquetDir: dir}}
	ctx := context.Background()

	writeParquet := func(day, rows string) {
		d := filepath.Join(dir, "events", "dt="+day)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		f := filepath.ToSlash(filepath.Join(d, "part.parquet"))
		if _, err := db.ExecContext(ctx, "COPY (SELECT * FROM (VALUES "+rows+") t(project_id, org_id, msg)) TO '"+f+"' (FORMAT parquet)"); err != nil {
			t.Fatal(err)
		}
	}
	// day1 mixes projects p1 and p2 (both org o1); day2 has only p2.
	writeParquet("2026-01-01", "('p1','o1','a'),('p2','o1','b')")
	writeParquet("2026-01-02", "('p2','o1','c')")

	count := func(where string) int64 {
		glob := filepath.ToSlash(filepath.Join(dir, "events", "dt=*", "part.parquet"))
		var n int64
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM read_parquet('"+glob+"') "+where).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}

	// Purge project p1: its rows must vanish, p2's must remain untouched.
	if err := m.PurgeColdScope(ctx, "project_id", "p1"); err != nil {
		t.Fatalf("purge project: %v", err)
	}
	if n := count("WHERE project_id='p1'"); n != 0 {
		t.Errorf("p1 rows survived cold purge: %d", n)
	}
	if n := count("WHERE project_id='p2'"); n != 2 {
		t.Errorf("p2 rows should be untouched, got %d", n)
	}

	// Purge org o1: removes everything; now-empty files are deleted.
	if err := m.PurgeColdScope(ctx, "org_id", "o1"); err != nil {
		t.Fatalf("purge org: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "events", "dt=*", "part.parquet")); len(left) != 0 {
		t.Errorf("empty parquet files should be removed, %d remain", len(left))
	}

	// An unexpected column is rejected (no SQL injection surface), and a nil
	// manager / empty tier is a safe no-op.
	if err := m.PurgeColdScope(ctx, "evil; DROP TABLE", "x"); err == nil {
		t.Error("invalid purge column should be rejected")
	}
	if err := (*Manager)(nil).PurgeColdScope(ctx, "project_id", "p1"); err != nil {
		t.Errorf("nil manager should be a no-op: %v", err)
	}
}
