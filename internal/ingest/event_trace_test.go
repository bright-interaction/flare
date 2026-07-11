package ingest

import "testing"

// TestParseEventTraceContext confirms the join key to a distributed trace is
// lifted off the Sentry payload's contexts.trace, so an error can later be
// correlated to its trace and the logs around it.
func TestParseEventTraceContext(t *testing.T) {
	raw := []byte(`{
		"event_id": "abc",
		"level": "error",
		"exception": {"values": [{"type": "Boom", "value": "kaboom"}]},
		"contexts": {"trace": {"trace_id": "0123456789abcdef0123456789abcdef", "span_id": "fedcba9876543210"}}
	}`)
	ev, err := ParseEvent(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.TraceID != "0123456789abcdef0123456789abcdef" {
		t.Errorf("trace_id = %q, want the payload's contexts.trace.trace_id", ev.TraceID)
	}
	if ev.SpanID != "fedcba9876543210" {
		t.Errorf("span_id = %q, want the payload's contexts.trace.span_id", ev.SpanID)
	}
}

// TestParseEventNoTraceContext confirms an event without contexts.trace simply
// carries empty ids (many bare messages and non-traced paths have no trace),
// which the ingest layer stores as SQL NULL.
func TestParseEventNoTraceContext(t *testing.T) {
	ev, err := ParseEvent([]byte(`{"event_id":"x","message":"hi"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.TraceID != "" || ev.SpanID != "" {
		t.Errorf("expected empty trace ids, got trace=%q span=%q", ev.TraceID, ev.SpanID)
	}
}
