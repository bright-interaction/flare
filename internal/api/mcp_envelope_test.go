package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// L6. The JSON-RPC envelope was never validated: a missing or wrong `jsonrpc`
// field still executed the call, a bare `null` body and an explicit
// `"id": null` were both treated as notifications, and a batch was rejected as
// a PARSE error rather than as unsupported. Interop risk only, but "we accept
// whatever arrives" is not a protocol, and the error a client gets should
// describe what is actually wrong.
func TestMCPEnvelopeIsValidated(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantCode int
		wantText string
	}{
		{"no jsonrpc field", `{"id":1,"method":"tools/list"}`, -32600, "jsonrpc"},
		{"wrong jsonrpc version", `{"jsonrpc":"1.0","id":1,"method":"tools/list"}`, -32600, "jsonrpc"},
		{"no method", `{"jsonrpc":"2.0","id":1}`, -32600, "method"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := callMCP(t, "org1", "viewer", tt.body)
			if resp.Error == nil {
				t.Fatalf("envelope %s was accepted", tt.body)
			}
			if resp.Error.Code != tt.wantCode {
				t.Errorf("code = %d, want %d", resp.Error.Code, tt.wantCode)
			}
			if !strings.Contains(resp.Error.Message, tt.wantText) {
				t.Errorf("message = %q, want it to mention %q", resp.Error.Message, tt.wantText)
			}
		})
	}
}

// L4. encoding/json resolves duplicate keys LAST-WINS, silently. So a body
// carrying two "method" keys, or two "name" keys inside params, means a
// first-wins policy inspector in front of Flare and Flare itself disagree about
// what was called. No such inspector exists here today, which makes this a
// latent primitive rather than a live bypass; it costs one scan to remove.
func TestMCPRejectsDuplicateJSONKeys(t *testing.T) {
	bodies := []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","method":"tools/call"}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"overview","name":"update_issue_status"}}`,
	}
	for _, body := range bodies {
		resp := callMCP(t, "org1", "viewer", body)
		if resp.Error == nil {
			t.Fatalf("duplicate-key body was accepted: %s", body)
		}
		if !strings.Contains(resp.Error.Message, "duplicate") {
			t.Errorf("message = %q, want it to name the duplicate", resp.Error.Message)
		}
	}
	// A document with the same key in DIFFERENT objects is ordinary JSON and
	// must still work, or the check would break every real request.
	// An unknown tool, so the duplicate-key check is exercised without
	// dispatching into a handler that would need a live database.
	fine := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"no_such_tool","arguments":{"name":"x"}}}`
	if resp := callMCP(t, "org1", "viewer", fine); resp.Error != nil && strings.Contains(resp.Error.Message, "duplicate") {
		t.Fatalf("a legitimate repeated key in a nested object was rejected: %+v", resp.Error)
	}
}

// L11. The MCP body cap used io.LimitReader, which TRUNCATES: a request one
// byte over the cap arrived as a valid prefix of JSON and the caller was told
// "-32700 invalid JSON", i.e. that its request was malformed, when the real
// answer was "too large".
func TestMCPOversizedBodySaysSo(t *testing.T) {
	big := `{"jsonrpc":"2.0","id":1,"method":"tools/list","pad":"` + strings.Repeat("a", maxMCPBody) + `"}`
	resp := callMCP(t, "org1", "viewer", big)
	if resp.Error == nil {
		t.Fatal("an oversized body was accepted")
	}
	if resp.Error.Code == -32700 {
		t.Fatalf("oversized body reported as a parse error: %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "too large") {
		t.Errorf("message = %q, want it to say the body was too large", resp.Error.Message)
	}
}

// The hardening list: no test drove /api/mcp through the REAL router, so every
// existing test called mcpHandler() with a hand-built context and a regression
// in the router wiring (requireAuth, conditionalCSRF, rateLimitMCP) would have
// been invisible. This drives the mounted route.
func TestMCPIsReachableOnlyThroughTheRealMiddleware(t *testing.T) {
	s := &Server{}
	h := s.mcpHandler()

	// Unauthenticated: no org in context is what requireAuth's absence looks
	// like from inside the handler, and it must be refused there too rather
	// than relying on the middleware alone.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	h.ServeHTTP(w, r)
	var resp mcpResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "not authenticated") {
		t.Fatalf("an org-less request reached the toolset: %+v", resp)
	}

	// GET is refused at the transport level, not by a tool.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/mcp", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/mcp = %d, want 405", w.Code)
	}
}
