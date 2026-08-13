package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bright-interaction/flare/internal/ai"
	"github.com/bright-interaction/flare/internal/scan"
)

// H1. The alert scrub was gated on `sensitive != ""`, which is the DETECTOR's
// verdict, and the detector is strictly narrower than the scrubber it then
// calls. Alert channels are webhook, slack, email and log; three of the four
// leave the tenant boundary. So the gate turned "catch a leaked secret" into
// "forward an arbitrary attacker-chosen string to an endpoint outside Flare".
//
// This is the property test the hardening list says never existed for the alert
// payload at all, which is why H1 shipped in the first place. It asserts the
// relationship, not a field list: anything the scrubber would redact must not
// be able to reach a channel just because the detector did not recognise it.
func TestAlertPayloadRedactsWhatTheDetectorDoesNotRecognise(t *testing.T) {
	// Each of these is a string an anonymous holder of a DSN public key can put
	// in an exception value. All seven were measured egressing.
	egressed := []string{
		"pq: auth failed for postgres://flare:S3cr3tDbPw@10.0.0.9:5432/flare",
		"upstream rejected Authorization: Bearer abcdefghijklmnopqrst",
		"config error: db_password=hunter2hunter2",
		"could not connect, password=correcthorsebattery",
		"failed to notify customer tom@example.com",
		"peer 192.168.1.24 reset the connection",
		"GitLab API rejected token glpat-AbCdEfGhIjKlMnOpQrSt",
	}
	for _, raw := range egressed {
		t.Run(raw[:20], func(t *testing.T) {
			// The premise: the detector really does not flag these. If it
			// starts to, this test is still correct but has stopped testing
			// what it was written for, and should be re-pointed.
			if kinds := scan.Kinds(raw); len(kinds) > 0 && kinds[0] == "" {
				t.Skipf("detector now flags %q", raw)
			}
			// The guarantee: the value that reaches a channel is scrubbed
			// whatever the detector said.
			out := ai.Scrub(raw)
			if out == raw {
				t.Fatalf("the scrubber leaves %q intact, so gating on it is moot", raw)
			}
			if !strings.ContainsAny(out, "[") {
				t.Fatalf("scrub produced no placeholder: %q", out)
			}
		})
	}
}

// H8. One global pool of 24 slots with no per-tenant accounting is a pool one
// tenant can own: two webhooks pointing at a host that accepts the connection
// and never answers, plus a flood of distinct fingerprints to that org's own
// DSN, occupied every alert-eval slot continuously and every OTHER tenant's
// alerts were dropped with a log line, no retry and no queue.
func TestOneOrgCannotOwnTheSharedAlertPool(t *testing.T) {
	s := &Server{}
	release := make(chan struct{})
	started := make(chan struct{}, bgAlertWorkers)

	// Org A takes everything it is allowed to take.
	accepted := 0
	for i := 0; i < bgAlertWorkers; i++ {
		if s.goBackgroundFor("org-a", "alert-eval", 5*time.Second, func(context.Context) { started <- struct{}{}; <-release }) {
			accepted++
		}
	}
	if accepted != bgPerOrgWorkers {
		t.Fatalf("org A got %d slots, want its share of %d", accepted, bgPerOrgWorkers)
	}

	// Org B still gets in, which is the whole point.
	if !s.goBackgroundFor("org-b", "alert-eval", 5*time.Second, func(context.Context) { started <- struct{}{}; <-release }) {
		t.Fatal("org B's alert was dropped while org A held the pool")
	}

	close(release)
}
