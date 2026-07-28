package partition

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestDropRunsInATransaction is a source-level guard, and it is source-level on
// purpose: the bug it covers is invisible at runtime.
//
// dropWithLockTimeout used to be
//
//	conn.Exec(ctx, "SET LOCAL lock_timeout = '5s'")
//	conn.Exec(ctx, "DROP TABLE IF EXISTS "+name)
//
// which reads exactly like a partition drop under a 5 second lock timeout, and
// is not one. SET LOCAL only applies inside a transaction block. Run outside
// one, Postgres emits `WARNING: SET LOCAL can only be used in transaction
// blocks` and discards it, because each statement is its own implicit
// transaction that ends before the next begins. The DROP then ran with the
// default lock_timeout of 0: wait forever.
//
// Nothing failed. The drop still succeeded on an idle database, so tests, CI
// and every manual check looked fine. It only bites in production, where DROP
// TABLE takes ACCESS EXCLUSIVE on the parent, queues behind one in-flight read,
// and then convoys every subsequent read and ingest INSERT behind itself for as
// long as that read runs.
//
// There is no unit test that can catch this without a live Postgres under
// concurrent load, so the property is asserted on the code: if a SET LOCAL is
// present, it must be issued on a transaction handle, not a bare connection.
func TestDropRunsInATransaction(t *testing.T) {
	src, err := os.ReadFile("partition.go")
	if err != nil {
		t.Fatalf("read partition.go: %v", err)
	}
	body := funcBody(t, string(src), "dropWithLockTimeout")

	if !strings.Contains(body, "SET LOCAL lock_timeout") {
		t.Fatal("dropWithLockTimeout no longer sets a lock_timeout. Without one, DROP TABLE " +
			"waits forever for ACCESS EXCLUSIVE and convoys every read and ingest INSERT behind it.")
	}

	// The SET LOCAL and the DROP must both go through a transaction handle.
	// `conn.Exec(... SET LOCAL ...)` is the exact shape that silently did nothing.
	bareSetLocal := regexp.MustCompile(`conn\.Exec\([^)]*SET LOCAL`)
	if bareSetLocal.MatchString(body) {
		t.Error("SET LOCAL is issued on a bare connection, not a transaction. Postgres warns and " +
			"DISCARDS it there, so the DROP silently runs with lock_timeout=0 (wait forever). " +
			"Wrap it: tx, _ := conn.Begin(ctx); tx.Exec(ctx, \"SET LOCAL ...\"); tx.Exec(ctx, \"DROP ...\").")
	}
	if !strings.Contains(body, "conn.Begin(") {
		t.Error("dropWithLockTimeout does not open a transaction; SET LOCAL cannot take effect without one")
	}
	if !strings.Contains(body, "tx.Exec") || !strings.Contains(body, "DROP TABLE") {
		t.Error("the DROP must run on the same transaction that set the lock_timeout")
	}
	if !strings.Contains(body, "tx.Commit(") {
		t.Error("the transaction is never committed, so the DROP would be rolled back")
	}
	if !strings.Contains(body, "tx.Rollback(") {
		t.Error("no rollback on the error paths: an early return would hold ACCESS EXCLUSIVE " +
			"until the pooled connection is reused, which is worse than the original bug")
	}
}

// TestEnsurePartitionAlsoBoundsItsLock covers the sibling.
//
// CREATE TABLE ... PARTITION OF takes ACCESS EXCLUSIVE on the PARENT exactly as
// DROP does, so an unbounded CREATE convoys reads and ingest the same way. The
// original audit finding named both; only the DROP was fixed, and a guard that
// covers one of two identical statements reads as covering the class.
func TestEnsurePartitionAlsoBoundsItsLock(t *testing.T) {
	src, err := os.ReadFile("partition.go")
	if err != nil {
		t.Fatalf("read partition.go: %v", err)
	}
	body := funcBody(t, string(src), "ensurePartition")

	if !strings.Contains(body, "SET LOCAL lock_timeout") {
		t.Error("ensurePartition creates a partition with no lock_timeout. CREATE TABLE ... " +
			"PARTITION OF takes ACCESS EXCLUSIVE on the parent, so it queues behind one " +
			"in-flight read and convoys every read and ingest INSERT behind it.")
	}
	if regexp.MustCompile(`pool\.Exec\(ctx, q\)`).MatchString(body) {
		t.Error("the CREATE still runs on a bare pool.Exec, outside any transaction, so a " +
			"SET LOCAL alongside it would be discarded by Postgres")
	}
	if !strings.Contains(body, "tx.Commit(") || !strings.Contains(body, "tx.Rollback(") {
		t.Error("ensurePartition must commit on success and roll back on the error paths, " +
			"or a failure holds ACCESS EXCLUSIVE until the pooled connection is reused")
	}
}

// funcBody returns the source of the named func, up to the next top-level func.
func funcBody(t *testing.T, src, name string) string {
	t.Helper()
	start := regexp.MustCompile(`(?m)^func (?:\([^)]*\) )?` + regexp.QuoteMeta(name) + `\(`).FindStringIndex(src)
	if start == nil {
		t.Fatalf("func %s not found in partition.go", name)
	}
	rest := src[start[1]:]
	if end := regexp.MustCompile(`(?m)^func `).FindStringIndex(rest); end != nil {
		return rest[:end[0]]
	}
	return rest
}
