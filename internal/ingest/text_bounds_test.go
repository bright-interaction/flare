package ingest

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// storedFieldBounds declares the byte cap for every client-supplied string on
// NormalizedEvent. Adding a field to that struct without adding it here fails
// TestEveryStoredEventFieldIsBounded, which is the point: "TEXT NOT NULL" in the
// migration is not a bound (Postgres TEXT holds up to 1 GB), so the bound has to
// live somewhere that a reviewer cannot forget.
var storedFieldBounds = map[string]int{
	"EventID":        maxEventIDBytes,
	"Level":          maxLevelBytes,
	"Platform":       maxPlatformBytes,
	"Environment":    maxEnvironmentBytes,
	"Release":        maxReleaseBytes,
	"Title":          titleMax,
	"Culprit":        maxCulpritBytes,
	"ExceptionType":  maxExceptionTypeBytes,
	"ExceptionValue": maxExceptionValueBytes,
	"Message":        maxMessageBytes,
	"TraceID":        maxTraceIDBytes,
	"SpanID":         maxTraceIDBytes,
}

// hugeEvent is a Sentry payload with every client-controlled string set to a
// megabyte of text, including the frame strings culprit is derived from.
func hugeEvent(t *testing.T) []byte {
	t.Helper()
	big := strings.Repeat("A", 1<<20)
	body := map[string]any{
		"event_id":    big,
		"level":       big,
		"platform":    big,
		"environment": big,
		"release":     big,
		"transaction": big,
		"message":     big,
		"contexts":    map[string]any{"trace": map[string]any{"trace_id": big, "span_id": big}},
		"exception": map[string]any{"values": []any{map[string]any{
			"type":  big,
			"value": big,
			"stacktrace": map[string]any{"frames": []any{map[string]any{
				"filename":     big,
				"function":     big,
				"module":       big,
				"context_line": big,
				"in_app":       true,
			}}},
		}}},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// TestEveryStoredEventFieldIsBounded is the twin guard for the text columns.
//
// titleMax bounded the derived title, and its own comment explains why: an 8 MiB
// exception message became an 8 MiB title that the issues list then returned 50
// of per request. That reasoning applies unchanged to culprit, level and
// platform, which sit in the same issues row and come back in the same list, and
// to message, exception_value, release and environment on the events row. The
// bound landed on one column and none of its siblings.
func TestEveryStoredEventFieldIsBounded(t *testing.T) {
	ev, err := ParseEvent(hugeEvent(t))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	rt := reflect.TypeOf(ev)
	rv := reflect.ValueOf(ev)
	checked := 0
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.Type.Kind() != reflect.String {
			continue // Frames and Raw are covered below / by the body cap
		}
		limit, declared := storedFieldBounds[f.Name]
		if !declared {
			t.Errorf("NormalizedEvent.%s is a client-supplied string with no declared "+
				"bound: it reaches a TEXT column unbounded", f.Name)
			continue
		}
		checked++
		if got := len(rv.Field(i).String()); got > limit {
			t.Errorf("%s = %d bytes after parsing a 1 MiB input, want <= %d",
				f.Name, got, limit)
		}
	}
	if checked != len(storedFieldBounds) {
		t.Errorf("checked %d fields against %d declared bounds: the table names a field "+
			"that no longer exists", checked, len(storedFieldBounds))
	}
}

// TestFrameTextIsBounded covers the stacktrace. Frame strings are stored in the
// stacktrace JSONB and, through culprit(), copied into the issues row, so an
// unbounded module or function name is the same finding one indirection away.
func TestFrameTextIsBounded(t *testing.T) {
	ev, err := ParseEvent(hugeEvent(t))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ev.Frames) != 1 {
		t.Fatalf("want 1 frame, got %d", len(ev.Frames))
	}
	f := ev.Frames[0]
	for name, got := range map[string]string{
		"Filename":    f.Filename,
		"Function":    f.Function,
		"Module":      f.Module,
		"ContextLine": f.ContextLine,
	} {
		if len(got) > maxFrameTextBytes {
			t.Errorf("Frame.%s = %d bytes, want <= %d", name, len(got), maxFrameTextBytes)
		}
	}
}

// TestSanitizeColumnIsTheBackstopForEveryPillar pins the bound where all four
// pillars already meet. Logs, spans and metrics never go through ParseEvent:
// their client text reaches Postgres through this one call, so that is where a
// bound covers a pillar added tomorrow without anyone remembering.
func TestSanitizeColumnIsTheBackstopForEveryPillar(t *testing.T) {
	if got := len(SanitizeColumn(strings.Repeat("A", 1<<20))); got > maxTextBytes {
		t.Errorf("SanitizeColumn returned %d bytes, want <= %d: a log body, span name or "+
			"metric name still reaches its column unbounded", got, maxTextBytes)
	}
	// A cut must not manufacture the invalid UTF-8 the function exists to remove.
	multi := strings.Repeat("ä", 1<<20) // 2 bytes per rune, so the cap lands mid-rune
	out := SanitizeColumn(multi)
	if len(out) > maxTextBytes {
		t.Errorf("multi-byte input returned %d bytes, want <= %d", len(out), maxTextBytes)
	}
	if !json.Valid([]byte(fmt.Sprintf("%q", out))) || strings.Contains(out, "�") {
		t.Error("truncation split a rune: the value Postgres rejects is exactly what this guards")
	}
}

// TestSanitizeJSONKeepsTheDocumentWhole is the other half of that split, and the
// reason the cap is not simply folded into SanitizeText.
//
// SanitizeText also runs over every string leaf and key of the payload JSONB.
// That document is what an operator opens to read the request body and
// breadcrumbs behind an error, so a length cap there would quietly delete the
// evidence the product exists to keep. A column and a document want different
// answers, and this asserts they still get them.
func TestSanitizeJSONKeepsTheDocumentWhole(t *testing.T) {
	big := strings.Repeat("B", 4*maxTextBytes)
	out := SanitizeJSON([]byte(`{"request":{"body":"` + big + `"}}`))

	var doc struct {
		Request struct {
			Body string `json:"body"`
		} `json:"request"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("sanitized payload is not valid JSON: %v", err)
	}
	if len(doc.Request.Body) != len(big) {
		t.Errorf("payload leaf was clipped to %d bytes from %d: the stored event no longer "+
			"holds what the client sent", len(doc.Request.Body), len(big))
	}
}
