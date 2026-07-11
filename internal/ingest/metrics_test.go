package ingest

import "testing"

// TestParseNativeMetrics covers the native JSON metric ingest: bare array,
// wrapped {"metrics":[...]}, default kind, and skipping nameless points.
func TestParseNativeMetrics(t *testing.T) {
	arr := `[{"name":"cpu","kind":"gauge","value":0.7},{"name":"reqs","value":42}]`
	recs, err := ParseNativeMetrics([]byte(arr))
	if err != nil {
		t.Fatalf("parse array: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
	if recs[0].Name != "cpu" || recs[0].Kind != "gauge" || recs[0].Value != 0.7 {
		t.Errorf("cpu record wrong: %+v", recs[0])
	}
	if recs[1].Kind != "gauge" { // defaulted
		t.Errorf("expected default kind gauge, got %q", recs[1].Kind)
	}

	wrapped := `{"metrics":[{"name":"mem","value":1.0}]}`
	recs, err = ParseNativeMetrics([]byte(wrapped))
	if err != nil || len(recs) != 1 || recs[0].Name != "mem" {
		t.Fatalf("wrapped form failed: %v %+v", err, recs)
	}

	// A nameless point is dropped, not stored.
	recs, _ = ParseNativeMetrics([]byte(`[{"value":9},{"name":"ok","value":1}]`))
	if len(recs) != 1 || recs[0].Name != "ok" {
		t.Errorf("nameless point should be skipped, got %+v", recs)
	}
}
