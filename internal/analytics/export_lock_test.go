package analytics

import (
	"database/sql"
	"errors"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestExportIsExcludedFromColdPurge is the regression test for erased telemetry
// coming back.
//
// The exporter wrote to the Parquet cold tier through e.duck.db with no lock at
// all, while PurgeColdScope, the right-to-erasure path, rewrites those same
// files under the exclusive lock. That let a delete interleave with an export:
//
//	exporter reads the hot partition; project P is still in it
//	the handler commits DELETE project P and answers 204
//	the handler purges the cold tier; dt=D only holds part.parquet.tmp, which
//	  the purge glob does not match, so it erases nothing there
//	the exporter renames part.parquet.tmp -> part.parquet
//
// P's telemetry is now in the cold tier, after Flare told the caller it was
// gone. The fix runs the export under the shared lock, so the purge waits for
// the rename and then erases the file the export produced.
//
// The test drives the lock directly rather than a real DuckDB export: the
// property under test is "these two cannot overlap", and asserting it on the
// mutex is what actually pins the fix in place.
func TestExportIsExcludedFromColdPurge(t *testing.T) {
	m := &Manager{}

	exportRunning := make(chan struct{})
	releaseExport := make(chan struct{})
	var purgeEntered bool
	var mu sync.Mutex

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = m.holdColdTier(func() error {
			close(exportRunning)
			// Hold the way a real COPY of a full day's partition does.
			<-releaseExport
			mu.Lock()
			defer mu.Unlock()
			if purgeEntered {
				t.Error("the cold-tier purge ran while an export was mid-flight; " +
					"that is the window where erased telemetry gets rewritten back")
			}
			return nil
		})
	}()

	<-exportRunning

	wg.Add(1)
	go func() {
		defer wg.Done()
		// Stands in for PurgeColdScope, which takes the exclusive lock.
		m.mu.Lock()
		defer m.mu.Unlock()
		mu.Lock()
		purgeEntered = true
		mu.Unlock()
	}()

	// Give the purge goroutine a chance to grab the lock if it can. It must not
	// be able to: the export still holds the shared side.
	time.Sleep(20 * time.Millisecond)
	close(releaseExport)
	wg.Wait()

	if !purgeEntered {
		t.Fatal("the purge never ran; the test proved nothing")
	}
}

// TestHoldColdTierFailsClosed locks the other half.
//
// A closed manager must surface an ERROR, never a nil "nothing to do". Partition
// retention drops a partition the moment ExportDay reports success, so returning
// nil here would drop a day of telemetry that was never archived. This is the
// same shape as the bug already fixed above in exportDay, where a transient
// DuckDB fault was being read as "the partition is empty".
func TestHoldColdTierFailsClosed(t *testing.T) {
	ran := false
	fn := func() error { ran = true; return nil }

	var nilManager *Manager
	if err := nilManager.holdColdTier(fn); !errors.Is(err, ErrClosed) {
		t.Errorf("nil manager: got %v, want ErrClosed (a nil error tells retention it is safe to DROP)", err)
	}

	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := &Manager{db: db, closed: true}
	if err := m.holdColdTier(fn); !errors.Is(err, ErrClosed) {
		t.Errorf("closed manager: got %v, want ErrClosed", err)
	}
	if ran {
		t.Error("the export body ran against a closed manager")
	}

	m.closed = false
	if err := m.holdColdTier(fn); err != nil {
		t.Errorf("open manager: %v", err)
	}
	if !ran {
		t.Error("the export body did not run on an open manager")
	}
}

// TestExportDayGoesThroughTheLock closes the gap the test above leaves open.
//
// Asserting that holdColdTier excludes the purge proves the helper works. It
// does NOT prove the exporter calls it, and the original bug was exactly that:
// the lock existed, PurgeColdScope took it correctly, and the export path
// simply never asked for it. Deleting the wrapper leaves every other test in
// this package green.
//
// So check the source. The DuckDB handle and the cold-tier files may only be
// touched from exportDay, which runs inside the closure, and ExportDay itself
// must do nothing but hand off to holdColdTier.
func TestExportDayGoesThroughTheLock(t *testing.T) {
	src, err := os.ReadFile("export.go")
	if err != nil {
		t.Fatalf("read export.go: %v", err)
	}
	body := exportedEntryPoint(t, string(src))

	if !strings.Contains(body, "holdColdTier(") {
		t.Fatal("ExportDay does not go through holdColdTier. Writing the Parquet cold tier " +
			"without the shared lock lets a right-to-erasure purge interleave with an export, " +
			"which rewrites erased telemetry back into the cold tier after Flare answered 204.")
	}
	for _, forbidden := range []string{"e.duck.db", "os.Rename", "os.MkdirAll", "e.pool."} {
		if strings.Contains(body, forbidden) {
			t.Errorf("ExportDay touches %s directly instead of inside the holdColdTier closure; "+
				"work outside the closure is work outside the lock", forbidden)
		}
	}
}

// exportedEntryPoint returns the body of func (e *Exporter) ExportDay, up to the
// start of the next top-level declaration.
func exportedEntryPoint(t *testing.T, src string) string {
	t.Helper()
	start := regexp.MustCompile(`(?m)^func \(e \*Exporter\) ExportDay\(`).FindStringIndex(src)
	if start == nil {
		t.Fatal("func (e *Exporter) ExportDay not found in export.go")
	}
	rest := src[start[1]:]
	if end := regexp.MustCompile(`(?m)^func `).FindStringIndex(rest); end != nil {
		return rest[:end[0]]
	}
	return rest
}
