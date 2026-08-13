package alerts

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

// The webhook URL is a bearer credential. redactChannelConfig/maskURL exist so
// it is never re-displayed, but a *url.Error carries the whole URL and that
// error was persisted to notification_channels.last_error and served on
// GET /api/channels, a VIEWER route. These assert the redaction happens at the
// one choke point every delivery error passes through.
func TestRedactDeliveryErrorHidesTheCredential(t *testing.T) {
	cases := []struct {
		name   string
		url    string
		secret string
	}{
		// Slack puts the credential in the PATH, which is why ai.Scrub is not
		// enough here: there is no key=value for it to match on.
		{"slack webhook path", "https://hooks.slack.com/services/T00000/B00000/SUPERSECRETPATH1234", "SUPERSECRETPATH1234"},
		{"generic webhook query", "https://webhook.example/ingest?token=SUPERSECRETTOKEN", "SUPERSECRETTOKEN"},
		{"url userinfo", "https://user:hunter2primary@webhook.example/ingest", "hunter2primary"},
	}
	for _, c := range cases {
		in := &url.Error{Op: "Post", URL: c.url, Err: errors.New("dial tcp: i/o timeout")}
		got := redactDeliveryError(in)
		if got == nil {
			t.Fatalf("%s: expected an error back", c.name)
		}
		msg := got.Error()
		if strings.Contains(msg, c.secret) {
			t.Errorf("%s: credential survived redaction: %s", c.name, msg)
		}
		// Redaction, not destruction: the operator still has to know which
		// endpoint failed and why.
		host, _ := url.Parse(c.url)
		if !strings.Contains(msg, host.Host) {
			t.Errorf("%s: host was destroyed, nothing left to debug: %s", c.name, msg)
		}
		if !strings.Contains(msg, "i/o timeout") {
			t.Errorf("%s: underlying cause was lost: %s", c.name, msg)
		}
	}
}

// A non-URL error must pass through untouched, or real diagnostics are lost.
func TestRedactDeliveryErrorPassesOtherErrorsThrough(t *testing.T) {
	if got := redactDeliveryError(nil); got != nil {
		t.Errorf("nil must stay nil, got %v", got)
	}
	in := errors.New("slack webhook returned status 500")
	if got := redactDeliveryError(in); got.Error() != in.Error() {
		t.Errorf("non-url error was rewritten: %q -> %q", in, got)
	}
}

// The redaction must sit in send(), not in each caller, so DispatchOne,
// Dispatch and the Recorder all inherit it. This drives the real send path with
// a channel type whose delivery will fail on a *url.Error.
func TestSendRedactsSoEveryCallerInherits(t *testing.T) {
	d := NewDispatcher(nil)
	secret := "SUPERSECRETPATH1234"
	ch := Channel{
		ID:     "chan-1",
		OrgID:  "org-1",
		Type:   "slack",
		Config: []byte(`{"webhook_url":"https://127.0.0.1/services/T0/B0/` + secret + `"}`),
	}

	var recorded string
	d.Recorder = func(_ context.Context, _, _ string, err error) {
		if err != nil {
			recorded = err.Error()
		}
	}

	err := d.DispatchOne(context.Background(), ch, Notification{Reason: "new_issue", Title: "boom"})
	if err == nil {
		t.Fatal("delivery to a blocked address should fail")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("DispatchOne returned the credential (this reaches POST /channels/{id}/test): %s", err)
	}
	if strings.Contains(recorded, secret) {
		t.Errorf("the Recorder got the credential (this is what lands in last_error): %s", recorded)
	}
	if recorded == "" {
		t.Fatal("nothing was recorded; the test is not exercising the record path")
	}
}
