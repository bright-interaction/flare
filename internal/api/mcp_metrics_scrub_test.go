package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bright-interaction/flare/internal/telemetry"
)

// TestQueryMetricsListScrubsAndLabels pins the query_metrics no-name/list branch
// at the MCP (LLM/BYOAI) boundary.
//
// Metric names are attacker-controllable: they arrive over the public OTLP
// ingest path keyed by a DSN public key that ships in a browser bundle, and only
// NUL/UTF-8 sanitisation touches them upstream, never the PII/secret scrubber. A
// name of "checkout aws_secret_access_key=AKIA..." must therefore leave the
// boundary with the credential gone, and the surface must carry the same
// trust:untrusted marker every sibling read tool attaches.
//
// This asserts on the marshalled bytes the handler hands back (metricNamesForMCP
// is the handler's exact output path), not on the source, so it fails if the
// scrub or the label is ever removed regardless of how the code reads.
func TestQueryMetricsListScrubsAndLabels(t *testing.T) {
	const secret = "AKIAIOSFODNN7EXAMPLE"
	names := []telemetry.MetricName{{
		Name:     "checkout aws_secret_access_key=" + secret,
		Kind:     "gauge",
		Points:   3,
		LastSeen: time.Now(),
	}}

	out := metricNamesForMCP(names)

	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal query_metrics list response: %v", err)
	}
	egress := string(b)

	// The secret must not survive to what egresses across the LLM boundary.
	if strings.Contains(egress, secret) {
		t.Errorf("AWS key survived to MCP egress: %s", egress)
	}

	// The surface must be labelled untrusted, like get_issue/list_issues/get_trace.
	if out["trust"] != "untrusted" {
		t.Errorf("query_metrics list response missing trust:untrusted, got %v", out["trust"])
	}
	if note, _ := out["note"].(string); note == "" {
		t.Error("query_metrics list response missing untrusted note")
	}

	// The scrub must be surgical: the legitimate part of the metric name has to
	// survive, or an agent loses the ability to read metrics at all.
	if !strings.Contains(egress, "checkout") {
		t.Errorf("scrub destroyed the legitimate metric name: %s", egress)
	}
	if !strings.Contains(egress, "[secret]") {
		t.Errorf("expected the credential to be replaced by a redaction marker: %s", egress)
	}
}
