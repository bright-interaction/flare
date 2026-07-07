package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactChannelConfig(t *testing.T) {
	// Slack + webhook URLs are bearer secrets: the secret path must not survive.
	cases := []struct {
		typ    string
		cfg    string
		secret string // must NOT appear in the redacted output
		keep   string // must still appear (host, for identification)
	}{
		{"slack", `{"webhook_url":"https://hooks.slack.com/services/T00/B00/XXXXsecretZZZZ"}`, "XXXXsecretZZZZ", "hooks.slack.com"},
		{"webhook", `{"url":"https://ops.example.com/hook/abc123secretTOKEN"}`, "abc123secretTOKEN", "ops.example.com"},
	}
	for _, c := range cases {
		out := string(redactChannelConfig(c.typ, json.RawMessage(c.cfg)))
		if strings.Contains(out, c.secret) {
			t.Errorf("%s: secret survived redaction: %q", c.typ, out)
		}
		if !strings.Contains(out, c.keep) {
			t.Errorf("%s: host dropped, cannot identify channel: %q", c.typ, out)
		}
	}

	// Email destination and log config are not secrets and are preserved.
	if out := string(redactChannelConfig("email", json.RawMessage(`{"to":"alerts@corp.com"}`))); !strings.Contains(out, "alerts@corp.com") {
		t.Errorf("email destination should be preserved: %q", out)
	}
}
