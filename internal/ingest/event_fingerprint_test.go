package ingest

import (
	"runtime"
	"strings"
	"testing"
)

// parseFP is the whole path under test: wire bytes -> grouping key. Going
// through ParseEvent rather than constructing a NormalizedEvent by hand is
// deliberate, because the bug being pinned was that the wire field was never
// READ, and a test that sets ClientFingerprint directly would have passed
// against the broken code.
func parseFP(t *testing.T, raw string) string {
	t.Helper()
	ev, err := ParseEvent([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return ev.Fingerprint()
}

// TestFingerprintHonoursClientOverride is the regression test for the
// alert-destroying bug: Hephaestus's ci-health watchdog sends a STABLE
// fingerprint with a message whose tail counter changes every tick. Grouping by
// message text minted a new issue every five minutes (372 in ten days). Same
// fingerprint + different message text must be ONE group.
func TestFingerprintHonoursClientOverride(t *testing.T) {
	a := parseFP(t, `{"event_id":"1","level":"error","fingerprint":["ci-health:hephaestus"],
		"message":"CI health RED [hephaestus]: head-of-queue has waited 1973s (>900)"}`)
	b := parseFP(t, `{"event_id":"2","level":"error","fingerprint":["ci-health:hephaestus"],
		"message":"CI health RED [hephaestus]: head-of-queue has waited 2423s (>900)"}`)
	if a != b {
		t.Errorf("same client fingerprint must group despite differing message text:\n a=%s\n b=%s", a, b)
	}
}

// TestFingerprintDistinctOverridesDoNotCollide is the other half: honouring the
// override must not over-group. Two different fingerprints stay two issues even
// when the message text is byte-identical.
func TestFingerprintDistinctOverridesDoNotCollide(t *testing.T) {
	a := parseFP(t, `{"event_id":"1","fingerprint":["ci-health:hephaestus"],"message":"same text"}`)
	b := parseFP(t, `{"event_id":"2","fingerprint":["ci-health:github"],"message":"same text"}`)
	if a == b {
		t.Error("different client fingerprints must not share a group")
	}
}

// TestFingerprintAbsentIsUnchanged pins the additive property the rollout
// depends on: an event with no fingerprint field groups exactly as it did
// before, so every existing issue keeps its identity and no backfill is needed.
func TestFingerprintAbsentIsUnchanged(t *testing.T) {
	ev, err := ParseEvent([]byte(`{"event_id":"1","message":"plain message"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.ClientFingerprint != nil {
		t.Errorf("no fingerprint field should leave ClientFingerprint nil, got %v", ev.ClientFingerprint)
	}
	if got, want := ev.Fingerprint(), ev.derivedFingerprint(); got != want {
		t.Errorf("absent override must equal the derived key: got %s want %s", got, want)
	}
}

// TestFingerprintDefaultTokenAlone confirms ["{{ default }}"] means EXACTLY the
// default. Hashing the token instead would silently move those events to a new
// group, the one outcome a caller writing "default" cannot have meant.
func TestFingerprintDefaultTokenAlone(t *testing.T) {
	ev, err := ParseEvent([]byte(`{"event_id":"1","fingerprint":["{{ default }}"],"message":"plain message"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got, want := ev.Fingerprint(), ev.derivedFingerprint(); got != want {
		t.Errorf("[{{ default }}] must equal the derived key: got %s want %s", got, want)
	}
}

// TestFingerprintDefaultTokenRefines covers the token's actual purpose:
// splicing the derived key into a larger key to SPLIT a group (per tenant, per
// shard) rather than replace it. Same message + different discriminator must
// separate; and the result must differ from the plain default.
func TestFingerprintDefaultTokenRefines(t *testing.T) {
	base := parseFP(t, `{"event_id":"0","message":"db down"}`)
	a := parseFP(t, `{"event_id":"1","fingerprint":["{{ default }}","tenant-a"],"message":"db down"}`)
	b := parseFP(t, `{"event_id":"2","fingerprint":["{{ default }}","tenant-b"],"message":"db down"}`)
	if a == b {
		t.Error("a discriminator after {{ default }} must split the group")
	}
	if a == base || b == base {
		t.Error("a refined key must not collide with the plain default key")
	}
}

// TestFingerprintEmptyPartsIgnored guards the collapse-everything failure mode.
// An all-empty fingerprint carries no grouping information, and treating it as
// a real key would merge every event in the project into a single issue --
// strictly worse than the bug being fixed.
func TestFingerprintEmptyPartsIgnored(t *testing.T) {
	for _, raw := range []string{
		`{"event_id":"1","fingerprint":[],"message":"plain message"}`,
		`{"event_id":"1","fingerprint":[""],"message":"plain message"}`,
		`{"event_id":"1","fingerprint":["","   "],"message":"plain message"}`,
	} {
		ev, err := ParseEvent([]byte(raw))
		if err != nil {
			t.Fatalf("parse %s: %v", raw, err)
		}
		if got, want := ev.Fingerprint(), ev.derivedFingerprint(); got != want {
			t.Errorf("empty override must fall back to derived for %s: got %s want %s", raw, got, want)
		}
	}
}

// TestFingerprintMalformedNeverDrops holds ingest's standing rule: an
// unexpected payload shape must never fail the event. Every case here must
// parse and fall back to the derived key. The bare-string case is the one
// exception -- an obvious client slip whose intent is unambiguous, so it is
// honoured rather than discarded.
func TestFingerprintMalformedNeverDrops(t *testing.T) {
	for _, raw := range []string{
		`{"event_id":"1","fingerprint":{"a":1},"message":"m"}`,
		`{"event_id":"1","fingerprint":123,"message":"m"}`,
		`{"event_id":"1","fingerprint":null,"message":"m"}`,
		`{"event_id":"1","fingerprint":[1,2],"message":"m"}`,
	} {
		ev, err := ParseEvent([]byte(raw))
		if err != nil {
			t.Fatalf("parse %s must not error: %v", raw, err)
		}
		if got, want := ev.Fingerprint(), ev.derivedFingerprint(); got != want {
			t.Errorf("malformed fingerprint %s must fall back to derived: got %s want %s", raw, got, want)
		}
	}

	ev, err := ParseEvent([]byte(`{"event_id":"1","fingerprint":"ci-health:hephaestus","message":"m"}`))
	if err != nil {
		t.Fatalf("bare-string fingerprint must not error: %v", err)
	}
	if len(ev.ClientFingerprint) != 1 || ev.ClientFingerprint[0] != "ci-health:hephaestus" {
		t.Errorf("bare-string fingerprint should be honoured, got %v", ev.ClientFingerprint)
	}
}

// TestFingerprintPartsAreBounded pins both caps. The fingerprint is
// client-controlled input that decides which row an event lands on, so an
// unbounded part count or part length is a memory and hash-cost lever.
func TestFingerprintPartsAreBounded(t *testing.T) {
	long := strings.Repeat("x", maxFingerprintPartBytes*3)
	ev, err := ParseEvent([]byte(`{"event_id":"1","fingerprint":["` + long + `"],"message":"m"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ev.ClientFingerprint) != 1 {
		t.Fatalf("want 1 part, got %d", len(ev.ClientFingerprint))
	}
	if got := len(ev.ClientFingerprint[0]); got > maxFingerprintPartBytes {
		t.Errorf("part not truncated: %d bytes > cap %d", got, maxFingerprintPartBytes)
	}

	var sb strings.Builder
	sb.WriteString(`{"event_id":"1","message":"m","fingerprint":[`)
	for i := 0; i < maxFingerprintParts*2; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`"p`)
		sb.WriteString(strings.Repeat("z", 1))
		sb.WriteString(string(rune('a' + i%26)))
		sb.WriteString(`"`)
	}
	sb.WriteString(`]}`)
	ev, err = ParseEvent([]byte(sb.String()))
	if err != nil {
		t.Fatalf("parse many: %v", err)
	}
	if got := len(ev.ClientFingerprint); got > maxFingerprintParts {
		t.Errorf("part count not capped: %d > cap %d", got, maxFingerprintParts)
	}
}

// TestFingerprintPartsCannotCollideAcrossBoundaries confirms the parts are
// joined with a separator that cannot occur inside a part, so ["ab","c"] and
// ["a","bc"] stay distinct instead of both hashing "abc".
func TestFingerprintPartsCannotCollideAcrossBoundaries(t *testing.T) {
	a := parseFP(t, `{"event_id":"1","fingerprint":["ab","c"],"message":"m"}`)
	b := parseFP(t, `{"event_id":"2","fingerprint":["a","bc"],"message":"m"}`)
	if a == b {
		t.Error(`["ab","c"] and ["a","bc"] must not collide`)
	}
}

// TestFingerprintAllocationIsBoundedByTheCap is the P0 regression test from the
// 2026-08-20 audit.
//
// parseFingerprint pre-sized its result with make([]string, 0, len(parts)),
// which reads naturally and hands a remote caller the allocation size. A
// gzipped body carrying a huge fingerprint array costs a few KB on the wire and
// hundreds of MB here, per concurrent request, for a field the loop was going
// to truncate to 32 entries anyway.
//
// Asserted via allocation measurement rather than by reading the constant,
// because the bug was precisely that the cap existed and the allocation did not
// use it.
func TestFingerprintAllocationIsBoundedByTheCap(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"event_id":"1","message":"m","fingerprint":[`)
	const huge = 200_000
	for i := 0; i < huge; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`""`)
	}
	sb.WriteString(`]}`)
	body := []byte(sb.String())

	// Every part is empty, so nothing survives filtering and the result must be
	// nil -- while the OLD code would still have reserved capacity for 200k
	// strings (~3 MB of headers alone) before discarding all of them.
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	ev, err := ParseEvent(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	runtime.ReadMemStats(&after)

	if ev.ClientFingerprint != nil {
		t.Errorf("all-empty fingerprint should filter to nil, got %d parts", len(ev.ClientFingerprint))
	}
	// The json.Unmarshal of the array itself is bounded by the request body
	// limit and is not what this guards; the extra copy is. Allow generous
	// headroom so this measures the regression, not the allocator.
	const maxExtra = 1 << 20
	if grew := after.TotalAlloc - before.TotalAlloc; grew > uint64(len(body))+maxExtra {
		t.Errorf("parsing a %d-byte body allocated %d bytes: the result slice is being "+
			"pre-sized from client-controlled len(parts) rather than from the %d-part cap",
			len(body), grew, maxFingerprintParts)
	}
}
