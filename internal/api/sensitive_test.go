package api

import (
	"strings"
	"testing"

	"github.com/bright-interaction/flare/internal/ingest"
)

func ev(msg string, frameCtx ...string) ingest.NormalizedEvent {
	e := ingest.NormalizedEvent{Message: msg}
	for _, c := range frameCtx {
		e.Frames = append(e.Frames, ingest.Frame{ContextLine: c})
	}
	return e
}

func TestDetectSensitive(t *testing.T) {
	cases := []struct {
		name string
		ev   ingest.NormalizedEvent
		want string // comma-joined kinds, "" for none
	}{
		{"clean error", ev("database timeout after 3 retries connecting to postgres:5432"), ""},
		{"order id is not a card", ev("order 1234567890123456 not found"), ""}, // 16 digits, fails Luhn
		{"jwt in message", ev("auth failed: token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"), "jwt"},
		{"github token in frame", ev("push failed", `token := "ghp_ABCDEFGHIJKLMNOPqrstuvwxyz012345"`), "secret"},
		{"aws key", ev("s3 upload denied for AKIAIOSFODNN7EXAMPLE"), "secret"},
		{"valid visa test card", ev("charge failed for 4111 1111 1111 1111"), "card"},
		{"secret and card", ev("charge 4111111111111111 with sk_live_REDACTED"), "card,secret"},
	}
	for _, c := range cases {
		got := strings.Join(detectSensitive(c.ev), ",")
		if got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestLuhnValid(t *testing.T) {
	if !luhnValid("4111 1111 1111 1111") {
		t.Error("known-valid test visa should pass Luhn")
	}
	if luhnValid("1234567890123456") {
		t.Error("arbitrary 16-digit number should fail Luhn")
	}
	if luhnValid("42") {
		t.Error("too short should fail")
	}
}
