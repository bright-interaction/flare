package flarereport

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestErrorAttrLeaksURLCredentials proves what the key-based redactor lets
// through. isSensitiveLogKey gates on the attribute KEY, but the key used
// everywhere in Go error logging is "error", which is not and must not be a
// sensitive key. The value under it is routinely a *url.Error whose exported
// URL field carries the endpoint and its query string.
//
// So the one line every service writes:
//
//	slog.Error("webhook delivery failed", "error", err)
//
// ships the full callback URL, token query and all, into the shared Flare logs
// store, which is a different trust boundary from the process that logged it.
//
// sentinel/internal/middleware/logship.go already found and fixed this with a
// second value-level layer (redact.Any). The other twelve copies of this file,
// flare's own included, still carry only the key layer.
func TestErrorAttrLeaksURLCredentials(t *testing.T) {
	sh := &logShipper{ch: make(chan nativeLogLine, 4)}
	h := &flareSlogHandler{next: slog.NewTextHandler(discard{}, nil), shipper: sh, minLvl: slog.LevelWarn}

	secret := "s3cr3t-webhook-token"
	err := &url.Error{
		Op:  "Post",
		URL: "https://hooks.partner.example.com/deliver?token=" + secret,
		Err: context.DeadlineExceeded,
	}

	r := slog.NewRecord(time.Now(), slog.LevelError, "webhook delivery failed", 0)
	r.Add("error", err)
	if e := h.Handle(context.Background(), r); e != nil {
		t.Fatalf("Handle: %v", e)
	}

	select {
	case line := <-sh.ch:
		got := string(line.Attributes)
		if strings.Contains(got, secret) {
			t.Errorf("the webhook token was shipped to the logs store in cleartext:\n  %s", got)
		}
		// The point is redaction, not destruction: an operator still has to be
		// able to tell WHICH endpoint failed, or the log record is worthless.
		if !strings.Contains(got, "hooks.partner.example.com") {
			t.Errorf("the endpoint was destroyed along with the token, leaving nothing to debug:\n  %s", got)
		}
		if !json.Valid(line.Attributes) {
			t.Errorf("attributes are not valid JSON after scrubbing:\n  %s", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing was enqueued; the test is not exercising the ship path")
	}
}

// TestErrorAttrLeakShapes covers the other values that ride in under a
// non-sensitive key. Each is a real shape from this estate's logs.
func TestErrorAttrLeakShapes(t *testing.T) {
	cases := []struct {
		name   string
		key    string
		value  any
		secret string
	}{
		{"dsn in a plain string", "detail", "connect failed: postgres://svc:hunter2primary@db:5432/flare", "hunter2primary"},
		{"bearer token in an error", "error", errString("GET /v1/x: 401 with Authorization: Bearer abcdef0123456789"), "abcdef0123456789"},
		{"api key in a nested struct", "request", struct{ URL string }{"https://api.vendor.io/v1?api_key=live_EXAMPLE_NOT_A_REAL_KEY"}, "live_EXAMPLE_NOT_A_REAL_KEY"},
	}
	for _, c := range cases {
		sh := &logShipper{ch: make(chan nativeLogLine, 4)}
		h := &flareSlogHandler{next: slog.NewTextHandler(discard{}, nil), shipper: sh, minLvl: slog.LevelWarn}
		r := slog.NewRecord(time.Now(), slog.LevelError, "call failed", 0)
		r.Add(c.key, c.value)
		if e := h.Handle(context.Background(), r); e != nil {
			t.Fatalf("%s: Handle: %v", c.name, e)
		}
		select {
		case line := <-sh.ch:
			if got := string(line.Attributes); strings.Contains(got, c.secret) {
				t.Errorf("%s: secret shipped in cleartext under non-sensitive key %q:\n  %s", c.name, c.key, got)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: nothing enqueued", c.name)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
