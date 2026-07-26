package ai

import (
	"encoding/json"
	"strings"
	"testing"
)

// Scrub is a TEXT scrubber. Running it over serialized JSON rewrote UNQUOTED
// numeric values (the 13-19 digit rule and the Luhn card rule), producing
// invalid JSON. Callers wrap the result in json.RawMessage, so the whole
// response then failed to marshal and the MCP layer rendered the []byte as a
// decimal byte dump. Nanosecond timestamps are 19 digits, so this fired on
// ordinary OTLP traffic.
func TestScrubJSONAlwaysReturnsValidJSON(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"nanosecond timestamp attribute", `{"http.request.duration_ns":1712345678901234567}`},
		{"otlp start time", `{"span.start_unix_nano":1690000000000000000}`},
		{"luhn-valid number", `{"order_id":4111111111111111}`},
		{"mixed", `{"msg":"ok","bytes_read":1234567890123,"user":"a@b.com"}`},
		{"nested arrays", `{"a":[{"b":1712345678901234567}],"c":["x@y.se"]}`},
		{"already clean", `{"k":"v","n":42}`},
		{"top-level array", `[{"ip":"10.0.0.1"},{"n":1712345678901234567}]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := ScrubJSON([]byte(tt.in))
			if !json.Valid(out) {
				t.Fatalf("ScrubJSON(%s) = %s, which is not valid JSON", tt.in, out)
			}
			// The whole point is that the result survives being embedded as a
			// json.RawMessage, which is where the original bug surfaced.
			if _, err := json.Marshal(struct {
				A json.RawMessage `json:"attributes"`
			}{json.RawMessage(out)}); err != nil {
				t.Fatalf("ScrubJSON(%s) result cannot be embedded: %v", tt.in, err)
			}
		})
	}
}

// String leaves must still be scrubbed; only the JSON structure is preserved.
func TestScrubJSONStillRedactsStringLeaves(t *testing.T) {
	out := string(ScrubJSON([]byte(`{"user":"tom@example.com","client":"192.168.1.24","note":"fine"}`)))
	if strings.Contains(out, "tom@example.com") {
		t.Fatalf("email survived scrubbing: %s", out)
	}
	if strings.Contains(out, "192.168.1.24") {
		t.Fatalf("ip survived scrubbing: %s", out)
	}
	if !strings.Contains(out, "fine") {
		t.Fatalf("harmless value was destroyed: %s", out)
	}
}

// Numbers must keep their JSON type rather than becoming a bare [number] token.
func TestScrubJSONPreservesNumericTypes(t *testing.T) {
	var got map[string]any
	if err := json.Unmarshal(ScrubJSON([]byte(`{"n":1712345678901234567}`)), &got); err != nil {
		t.Fatalf("result did not unmarshal: %v", err)
	}
	if _, ok := got["n"].(float64); !ok {
		t.Fatalf("numeric value lost its type: %#v", got["n"])
	}
}

// A stored blob that is not valid JSON must still be redacted, not passed
// through raw, and must not be emitted as something a caller would embed.
func TestScrubJSONHandlesNonJSON(t *testing.T) {
	out := string(ScrubJSON([]byte(`not json at all: tom@example.com`)))
	if strings.Contains(out, "tom@example.com") {
		t.Fatalf("email survived on the non-JSON path: %s", out)
	}
	if got := ScrubJSON(nil); len(got) != 0 {
		t.Fatalf("ScrubJSON(nil) = %q, want empty", got)
	}
}
