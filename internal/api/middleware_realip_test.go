package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRealIPTrustedProxyOnly(t *testing.T) {
	h := realIP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(clientIP(r)))
	}))
	call := func(remote, xff string) string {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Body.String()
	}

	// A directly-connected public client MUST NOT be able to spoof X-Forwarded-For:
	// the real peer wins, so it cannot rotate its apparent IP to dodge the lockout.
	if got := call("203.0.113.9:5000", "1.2.3.4"); got != "203.0.113.9" {
		t.Errorf("public peer's spoofed XFF was honored: got %q, want 203.0.113.9", got)
	}
	// Behind a trusted (loopback/private) proxy, the right-most non-proxy XFF
	// entry is the real client.
	if got := call("127.0.0.1:5000", "9.9.9.9, 10.0.0.2"); got != "9.9.9.9" {
		t.Errorf("proxy XFF not honored: got %q, want 9.9.9.9", got)
	}
	if got := call("172.18.0.5:33000", "198.51.100.7"); got != "198.51.100.7" {
		t.Errorf("private-proxy XFF not honored: got %q, want 198.51.100.7", got)
	}
	// Trusted proxy but no XFF: fall back to the peer.
	if got := call("127.0.0.1:5000", ""); got != "127.0.0.1" {
		t.Errorf("no-XFF fallback wrong: got %q, want 127.0.0.1", got)
	}
}
