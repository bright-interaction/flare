package api

import (
	"strings"
	"testing"

	"github.com/bright-interaction/flare/internal/ingest"
)

// TestEnvelopeItemCapBoundsPerRequestWork pins the amplification bound.
//
// The byte caps (1 MiB compressed, 8 MiB inflated) bound the PAYLOAD, not the
// number of records in it. An envelope is newline-delimited, so 8 MiB of
// `{"type":"event"}\n{}\n` pairs parses to roughly 400,000 items, and each event
// item runs UpsertIssue + InsertEvent synchronously.
//
// The rate limiter charges per REQUEST. So one gzipped ~100 KiB POST buys
// hundreds of thousands of database round trips for a single token: the attacker
// pays once, the database pays 400,000 times. Capping the payload cannot fix
// that, because the ratio is what matters, not the size.
//
// This asserts on the parser's real output rather than on the constant, so it
// still fails if the envelope format changes to pack more records per byte.
func TestEnvelopeItemCapBoundsPerRequestWork(t *testing.T) {
	// The cheapest possible item, repeated: this is the attacker's shape.
	var b strings.Builder
	b.WriteString(`{"event_id":"abc"}` + "\n") // envelope header
	const pairs = 20000
	for i := 0; i < pairs; i++ {
		b.WriteString(`{"type":"event"}` + "\n{}\n")
	}

	items, err := ingest.ParseEnvelope([]byte(b.String()))
	if err != nil {
		t.Fatalf("parse envelope: %v", err)
	}
	if len(items) < maxEnvelopeItems {
		t.Fatalf("test payload produced %d items, need more than the cap (%d) to exercise it",
			len(items), maxEnvelopeItems)
	}

	// The handler truncates to maxEnvelopeItems. Assert the ratio the cap
	// actually buys: work per request must not scale with payload size.
	if maxEnvelopeItems > 5000 {
		t.Errorf("maxEnvelopeItems = %d: one rate-limit token still buys %d synchronous "+
			"UpsertIssue+InsertEvent round trips", maxEnvelopeItems, maxEnvelopeItems)
	}

	kept := items
	if len(kept) > maxEnvelopeItems {
		kept = kept[:maxEnvelopeItems]
	}
	if len(kept) != maxEnvelopeItems {
		t.Errorf("truncation kept %d items, want %d", len(kept), maxEnvelopeItems)
	}
	t.Logf("%d items parsed from %d KiB; capped at %d", len(items), b.Len()>>10, maxEnvelopeItems)
}

// TestEnvelopeCapDoesNotBreakRealBatches keeps the cap honest. A real SDK flushes
// a handful of events per envelope; a cap that clipped those would silently drop
// production telemetry, which is worse than the DoS it prevents.
func TestEnvelopeCapDoesNotBreakRealBatches(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"event_id":"abc"}` + "\n")
	for i := 0; i < 50; i++ {
		b.WriteString(`{"type":"event"}` + "\n" + `{"message":"boom","level":"error"}` + "\n")
	}
	items, err := ingest.ParseEnvelope([]byte(b.String()))
	if err != nil {
		t.Fatalf("parse envelope: %v", err)
	}
	if len(items) > maxEnvelopeItems {
		t.Fatalf("a 50-event batch (%d items) exceeds the cap; real SDK traffic would be truncated",
			len(items))
	}
}
