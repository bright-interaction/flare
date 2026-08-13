package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bright-interaction/flare/internal/telemetry"
)

const leakedJWT = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhZG1pbiJ9.sIgNaTuRe1234"

// C1. detectSensitive reads a frame's context line. The REST response scrubbed
// the message and the exception value and passed the stack trace through
// verbatim, so the single most common way the flag fires produced a flagged
// issue whose flagged value was served raw by the endpoint the flag exists to
// protect: readable by role viewer and by every read-only API key, with a
// "sensitive data" badge telling the reader where to look.
func TestFlaggedIssueRedactsTheStackTraceThatTriggeredTheFlag(t *testing.T) {
	stack := json.RawMessage(`{"frames":[{"filename":"app.js","context_line":"const t = \"` + leakedJWT + `\""}]}`)
	events := []telemetry.Event{{
		ID: "ev1", Level: "error", Message: "boom",
		Stacktrace: stack, ReceivedAt: time.Now(),
	}}

	s := &Server{}
	out := s.toEventResponses(context.Background(), "iss1", "org1", events, true)
	if len(out) != 1 {
		t.Fatalf("want 1 event, got %d", len(out))
	}
	if strings.Contains(string(out[0].Stacktrace), leakedJWT) {
		t.Fatalf("flagged issue served the JWT verbatim in the stack trace: %s", out[0].Stacktrace)
	}
	if !strings.Contains(string(out[0].Stacktrace), "[jwt]") {
		t.Fatalf("stack trace was not scrubbed at all: %s", out[0].Stacktrace)
	}

	// The unflagged path is unchanged: an authenticated operator of the org
	// still sees the raw event on the dashboard.
	raw := s.toEventResponses(context.Background(), "iss1", "org1", events, false)
	if !strings.Contains(string(raw[0].Stacktrace), leakedJWT) {
		t.Fatalf("unflagged issue should keep the raw stack trace for the operator")
	}
}

// C1, issue half. AITriage is model output derived from the leaked value and
// was served raw on a flagged issue.
func TestFlaggedIssueRedactsEveryTextField(t *testing.T) {
	got := toIssueResponse(telemetry.Issue{
		ID: "i1", Title: "Error: " + leakedJWT, Culprit: "auth.js",
		AITriage:  "The token " + leakedJWT + " is expired.",
		Sensitive: "jwt",
	})
	if strings.Contains(got.Title, leakedJWT) || strings.Contains(got.AITriage, leakedJWT) {
		t.Fatalf("flagged issue leaked the JWT: title=%q ai_triage=%q", got.Title, got.AITriage)
	}
}

// H3. Twelve attacker-writable fields crossed the LLM boundary unscrubbed, in
// four tools whose siblings WERE scrubbed. The boundary scrubs by structure
// now, so this asserts the property rather than a field list: nothing a client
// can write survives, whatever it is called.
func TestMCPBoundaryScrubsEveryClientWrittenField(t *testing.T) {
	type span struct {
		Name         string          `json:"name"`
		Kind         string          `json:"kind"`
		Status       string          `json:"status"`
		SpanID       string          `json:"span_id"`
		ParentSpanID string          `json:"parent_span_id"`
		Attributes   json.RawMessage `json:"attributes"`
	}
	in := map[string]any{
		"spans": []span{{
			Name:         "GET " + leakedJWT,
			Kind:         "server " + leakedJWT,
			Status:       "error " + leakedJWT,
			SpanID:       leakedJWT,
			ParentSpanID: leakedJWT,
			Attributes:   json.RawMessage(`{"http.header":"Bearer abcdefghijklmnop"}`),
		}},
		"trust": "untrusted",
		"note":  untrustedTelemetryNote,
	}

	out := mcpJSON(scrubForMCP(in))
	if strings.Contains(out, leakedJWT) {
		t.Fatalf("a client-written field crossed the LLM boundary unscrubbed:\n%s", out)
	}
	if strings.Contains(out, "Bearer abcdefghijklmnop") {
		t.Fatalf("span attributes crossed unscrubbed:\n%s", out)
	}
	// Flare's own labels are not telemetry and must survive intact, or the
	// prompt-injection labelling this boundary depends on becomes noise.
	if !strings.Contains(out, untrustedTelemetryNote) {
		t.Fatalf("the trust label was mangled by its own scrubber:\n%s", out)
	}
}

// H10. Releases in this estate are git SHAs. Blanket-scrubbing every 40-hex run
// made `release` and `first_release` read "[hash]" for the agent the field was
// scrubbed for, with no signal the value had been rewritten, while source-map
// symbolication kept working off the unscrubbed value stored upstream.
func TestMCPKeepsReleaseIdentifiersReadable(t *testing.T) {
	const sha = "a1017dea7f3c9b2e8d5a4f6c1b0e9d8a7c6b5a4f"
	got := scrubIssueForMCP(issueResponse{ID: "i1", FirstRelease: sha, Title: "boom"})
	if got.FirstRelease != sha {
		t.Fatalf("first_release = %q, want the git SHA intact", got.FirstRelease)
	}
	events := scrubEventsForMCP([]eventResponse{{ID: "e1", Release: sha}})
	if events[0].Release != sha {
		t.Fatalf("release = %q, want the git SHA intact", events[0].Release)
	}
	// A credential parked in a release field is still redacted.
	leaky := scrubEventsForMCP([]eventResponse{{ID: "e1", Release: "glpat-AbCdEfGhIjKlMnOpQrSt"}})
	if !strings.Contains(leaky[0].Release, "[secret]") {
		t.Fatalf("release = %q, want the credential redacted", leaky[0].Release)
	}
}
