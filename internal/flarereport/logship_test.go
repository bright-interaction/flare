package flarereport

import (
	"fmt"
	"log/slog"
	"testing"
	"time"
)

// TestParseDSNForLogs locks the DSN -> native-logs endpoint mapping the log
// shipper routes on; a wrong endpoint or key means logs silently never land.
func TestParseDSNForLogs(t *testing.T) {
	endpoint, key, ok := parseDSNForLogs("https://pubkey123@flare.example.com/42")
	if !ok {
		t.Fatal("valid DSN should parse")
	}
	if endpoint != "https://flare.example.com/api/42/logs" {
		t.Errorf("endpoint = %q", endpoint)
	}
	if key != "pubkey123" {
		t.Errorf("key = %q", key)
	}

	for _, bad := range []string{"", "not a url", "https://flare.example.com/42", "https://pubkey@flare.example.com/"} {
		if _, _, ok := parseDSNForLogs(bad); ok {
			t.Errorf("expected %q to be rejected", bad)
		}
	}
}

// TestLogShipperBreaksDefaultHandlerCycle reproduces the startup deadlock that a
// bare default-handler service (hephaestus, svar) hit: installLogShipper wrapped
// slog's built-in defaultHandler and SetDefault-ed the wrapper, forming the cycle
// wrapper -> defaultHandler -> std log -> wrapper, which self-deadlocked the
// non-reentrant std log mutex on the first log line and hung the process before it
// bound its port. The fix passes through to a concrete stderr handler when the
// current handler is the built-in default. Assert the passthrough is never the
// recursive default handler, and that a real log call actually returns.
func TestLogShipperBreaksDefaultHandlerCycle(t *testing.T) {
	t.Setenv("FLARE_DSN", "https://k@flare.example.com/1")
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	// installLogShipperOnce is the un-Once-guarded core; call it directly so the
	// test drives the install. A fresh test binary starts on slog's built-in
	// default handler, which is exactly the vulnerable baseline.
	installLogShipperOnce("svar-test")

	h, ok := slog.Default().Handler().(*flareSlogHandler)
	if !ok {
		t.Fatalf("expected default handler to be *flareSlogHandler after install, got %T", slog.Default().Handler())
	}
	if got := fmt.Sprintf("%T", h.next); got == "*slog.defaultHandler" {
		t.Fatalf("passthrough handler is still slog's built-in default (%s): the recursive cycle is not broken", got)
	}

	// A real log call must return; a regression self-deadlocks, and the watchdog
	// fails the test instead of hanging the whole suite.
	done := make(chan struct{})
	go func() {
		slog.Info("cycle probe", "k", "v")
		slog.Warn("cycle probe warn")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("log call did not return within 5s: the default-handler wrap deadlock regressed")
	}
}
