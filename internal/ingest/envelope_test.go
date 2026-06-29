package ingest

import "testing"

// Exact 3-line envelope @sentry/node v8 sends to /api/{id}/envelope/.
const sentrySDKEnvelope = `{"event_id":"ce17406a98f04de684ef6fd1f6130abd","sent_at":"2026-06-29T20:44:31.984Z","sdk":{"name":"sentry.javascript.node","version":"8.55.2"},"trace":{"environment":"production","public_key":"testkey123","trace_id":"da40c58c887c4dd586909b6cb64196b1"}}
{"type":"event"}
{"exception":{"values":[{"type":"Error","value":"E2E real-sdk: payment gateway timeout"}]},"event_id":"ce17406a98f04de684ef6fd1f6130abd","level":"error","platform":"node"}`

func TestParseEnvelopeSentrySDK(t *testing.T) {
	payloads, err := ParseEnvelope([]byte(sentrySDKEnvelope))
	if err != nil {
		t.Fatalf("ParseEnvelope error: %v", err)
	}
	if len(payloads) != 1 {
		t.Fatalf("expected 1 event payload, got %d", len(payloads))
	}
	ev, err := ParseEvent(payloads[0])
	if err != nil {
		t.Fatalf("ParseEvent error: %v", err)
	}
	if ev.ExceptionType != "Error" {
		t.Fatalf("expected exception type Error, got %q (title %q)", ev.ExceptionType, ev.Title)
	}
}
