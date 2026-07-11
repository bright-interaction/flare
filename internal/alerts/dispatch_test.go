package alerts

import (
	"context"
	"encoding/json"
	"testing"
)

// TestDispatchOneRecordsOutcome covers the delivery-status plumbing added for
// the "send test" action and per-channel health: DispatchOne returns the real
// outcome and records it, error paths surface an error, and the recorder is
// skipped for an empty channel id. No network: the log/unsupported/misconfig
// paths never dial, so they dodge the SSRF-guarded client (which blocks the
// loopback an httptest server would use).
func TestDispatchOneRecordsOutcome(t *testing.T) {
	d := NewDispatcher(nil)
	var gotOrg, gotID string
	var gotErr error
	called := 0
	d.Recorder = func(_ context.Context, org, id string, err error) {
		called++
		gotOrg = org
		gotID = id
		gotErr = err
	}

	// log channel always succeeds and records a nil error.
	if err := d.DispatchOne(context.Background(), Channel{ID: "c1", OrgID: "o1", Type: "log"}, Notification{}); err != nil {
		t.Fatalf("log dispatch: unexpected error %v", err)
	}
	if called != 1 || gotID != "c1" || gotOrg != "o1" || gotErr != nil {
		t.Fatalf("log: called=%d org=%q id=%q err=%v", called, gotOrg, gotID, gotErr)
	}

	// unsupported channel type returns and records an error.
	if err := d.DispatchOne(context.Background(), Channel{ID: "c2", OrgID: "o1", Type: "carrier-pigeon"}, Notification{}); err == nil {
		t.Fatal("unsupported type: expected error")
	}
	if called != 2 || gotID != "c2" || gotErr == nil {
		t.Fatalf("unsupported: called=%d id=%q err=%v", called, gotID, gotErr)
	}

	// misconfigured webhook (no url) errors before any dial.
	if err := d.DispatchOne(context.Background(), Channel{ID: "c3", OrgID: "o1", Type: "webhook", Config: json.RawMessage(`{}`)}, Notification{}); err == nil {
		t.Fatal("webhook without url: expected error")
	}
	if called != 3 || gotErr == nil {
		t.Fatalf("webhook misconfig: called=%d err=%v", called, gotErr)
	}

	// an empty channel id must NOT invoke the recorder (ad-hoc dispatch).
	if err := d.DispatchOne(context.Background(), Channel{ID: "", OrgID: "o1", Type: "log"}, Notification{}); err != nil {
		t.Fatalf("empty-id log dispatch: unexpected error %v", err)
	}
	if called != 3 {
		t.Fatalf("empty id should skip recorder, called=%d", called)
	}
}
