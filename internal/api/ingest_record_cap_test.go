package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/bright-interaction/flare/internal/ingest"
)

// TestOTLPBodyEncodesFarMoreRowsThanOneTokenShouldBuy pins the amplification the
// per-request row budget exists to bound.
//
// maxEnvelopeItems bounds the ITEMS in a Sentry envelope, and on the events path
// one item is one row, so events were covered. Every other pillar fans one item
// out to an unbounded number of rows: an OTLP export nests resource -> scope ->
// record, so the record count is limited only by the byte cap. Nothing bounded
// the rows.
//
// The rate limiter charges per REQUEST, and the DSN public key ships in the
// browser bundle of every project, so this is reachable with no account.
//
// Measured against the real parsers, then scaled to maxDecompressedBody, which
// is the largest body a request can inflate to.
func TestOTLPBodyEncodesFarMoreRowsThanOneTokenShouldBuy(t *testing.T) {
	const sample = 1 << 20 // 1 MiB measured, scaled to the 8 MiB inflate cap

	// The cheapest record the parser accepts, repeated: the attacker's shape.
	empties := strings.TrimSuffix(strings.Repeat("{},", sample/3), ",")

	cases := []struct {
		pillar string
		body   string
		count  func(string) (int, error)
	}{
		{
			pillar: "otlp logs",
			body:   `{"resourceLogs":[{"scopeLogs":[{"logRecords":[` + empties + `]}]}]}`,
			count: func(b string) (int, error) {
				r, err := ingest.ParseOTLPLogs([]byte(b), true)
				return len(r), err
			},
		},
		{
			pillar: "otlp traces",
			body:   `{"resourceSpans":[{"scopeSpans":[{"spans":[` + empties + `]}]}]}`,
			count: func(b string) (int, error) {
				r, err := ingest.ParseOTLPTraces([]byte(b), true)
				return len(r), err
			},
		},
		{
			pillar: "otlp metrics",
			body: `{"resourceMetrics":[{"scopeMetrics":[{"metrics":[{"name":"m","gauge":{"dataPoints":[` +
				strings.TrimSuffix(strings.Repeat(`{"asDouble":1},`, sample/15), ",") + `]}}]}]}]}`,
			count: func(b string) (int, error) {
				r, err := ingest.ParseOTLPMetrics([]byte(b), true)
				return len(r), err
			},
		},
		{
			pillar: "native logs",
			body:   `[` + empties + `]`,
			count: func(b string) (int, error) {
				r, err := ingest.ParseNativeLogs([]byte(b))
				return len(r), err
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.pillar, func(t *testing.T) {
			n, err := tc.count(tc.body)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			// Multiply before dividing: the per-byte record density is fractional,
			// and rounding the ratio down first understates the amplification by
			// an eighth.
			atCap := n * maxDecompressedBody / len(tc.body)
			t.Logf("%s: %d rows from %d KiB -> %d rows at the %d MiB inflate cap",
				tc.pillar, n, len(tc.body)>>10, atCap, maxDecompressedBody>>20)

			if atCap <= maxIngestRecords {
				t.Fatalf("%s: payload only reaches %d rows, below the cap (%d): "+
					"this test no longer exercises the amplification", tc.pillar, atCap, maxIngestRecords)
			}
			// The bound that matters is the RATIO, not the size: work per request
			// must not scale with payload size.
			if got := newIngestBudget().take(atCap); got != maxIngestRecords {
				t.Errorf("%s: budget allowed %d rows for one request, want %d",
					tc.pillar, got, maxIngestRecords)
			}
		})
	}
}

// TestIngestBudgetIsPerRequestNotPerCall is the envelope case. handleEnvelope
// calls persistSpans once per transaction item, up to maxEnvelopeItems times, so
// a cap applied per CALL would still let one request write
// maxEnvelopeItems x cap rows. The budget is threaded through the whole request
// for exactly that reason.
func TestIngestBudgetIsPerRequestNotPerCall(t *testing.T) {
	b := newIngestBudget()
	const perItem = 1000 // Sentry's own per-transaction span ceiling
	total := 0
	for i := 0; i < maxEnvelopeItems; i++ {
		total += b.take(perItem)
	}
	if total != maxIngestRecords {
		t.Errorf("%d envelope items of %d spans wrote %d rows, want the request cap %d",
			maxEnvelopeItems, perItem, total, maxIngestRecords)
	}
	if want := maxEnvelopeItems*perItem - maxIngestRecords; b.dropped != want {
		t.Errorf("dropped = %d, want %d", b.dropped, want)
	}
}

// TestIngestCapDoesNotBreakRealProducers keeps the cap honest. Silently
// truncating real telemetry is worse than the DoS it prevents, so the cap must
// sit above every producer that actually ships.
func TestIngestCapDoesNotBreakRealProducers(t *testing.T) {
	producers := []struct {
		name  string
		batch int
	}{
		{"otel collector batch processor default send_batch_size", 8192},
		{"sentry per-transaction span ceiling", 1000},
		{"a chatty native SDK flush", 500},
	}
	for _, p := range producers {
		if got := newIngestBudget().take(p.batch); got != p.batch {
			t.Errorf("%s: %d records truncated to %d; real traffic would be dropped",
				p.name, p.batch, got)
		}
	}
}

// TestOTLPPartialSuccessReportsDrops asserts the drop is reported on the wire.
// OTLP has partial_success for precisely this: without it a collector counts
// every dropped row as delivered, which is the silent-loss shape an
// observability product must never ship. Empty body when nothing was dropped,
// because partial_success present-but-zero is a "some failed" signal to some
// collectors.
func TestOTLPPartialSuccessReportsDrops(t *testing.T) {
	if got := otlpResponse(0, otlpRejectedLogRecords); len(got) != 0 {
		t.Errorf("no drops should answer an empty body, got %v", got)
	}
	got := otlpResponse(7, otlpRejectedSpans)
	ps, ok := got["partialSuccess"].(map[string]any)
	if !ok {
		t.Fatalf("missing partialSuccess in %v", got)
	}
	// OTLP/JSON follows the proto3 JSON mapping: int64 fields are STRINGS.
	if ps[otlpRejectedSpans] != "7" {
		t.Errorf("%s = %v (%T), want the string \"7\"",
			otlpRejectedSpans, ps[otlpRejectedSpans], ps[otlpRejectedSpans])
	}
	if msg, _ := ps["errorMessage"].(string); msg == "" {
		t.Error("errorMessage empty: the collector operator gets no reason for the drop")
	}
}

// TestEveryPersistPathSpendsTheBudget is the twin guard.
//
// This whole audit found one shape over and over: a guard that exists in one
// place and not its sibling. maxEnvelopeItems was itself an instance, fixed on
// the events path while logs, metrics and spans stayed unbounded beside it. So
// the rule is enforced structurally rather than by review: every persist* method
// on *Server must spend the request budget, and a new pillar added tomorrow
// fails this test until it does.
func TestEveryPersistPathSpendsTheBudget(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()

	found := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || !strings.HasPrefix(fn.Name.Name, "persist") {
				continue
			}
			found++
			if !callsBudgetTake(fn) {
				t.Errorf("%s: %s does not spend the ingest budget, so one request "+
					"can write an unbounded number of rows through it", name, fn.Name.Name)
			}
		}
	}
	// A rename that made the scan match nothing would leave this test green while
	// checking nothing at all.
	if found < 3 {
		t.Fatalf("found %d persist* methods, expected at least 3 (logs, metrics, spans): "+
			"the scan is no longer finding the code it guards", found)
	}
}

func callsBudgetTake(fn *ast.FuncDecl) bool {
	spends := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "take" {
			spends = true
		}
		return true
	})
	return spends
}
