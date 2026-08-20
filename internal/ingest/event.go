// Package ingest parses incoming error payloads (Sentry-wire compatible) into
// Flare's normalized event shape and computes the grouping fingerprint. It is
// pure: no database, no HTTP. The api layer handles transport and persistence.
package ingest

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

// Frame is one stack frame, a subset of the Sentry frame interface.
type Frame struct {
	Filename    string `json:"filename,omitempty"`
	Function    string `json:"function,omitempty"`
	Module      string `json:"module,omitempty"`
	Lineno      int    `json:"lineno,omitempty"`
	Colno       int    `json:"colno,omitempty"`
	InApp       *bool  `json:"in_app,omitempty"`
	ContextLine string `json:"context_line,omitempty"`
}

func (f Frame) inApp() bool { return f.InApp == nil || *f.InApp }

// NormalizedEvent is the internal representation persisted to the events table.
type NormalizedEvent struct {
	EventID        string
	Level          string
	Platform       string
	Environment    string
	Release        string
	Title          string
	Culprit        string
	ExceptionType  string
	ExceptionValue string
	Message        string
	Frames         []Frame
	TraceID        string
	SpanID         string
	Raw            json.RawMessage

	// ClientFingerprint is the grouping key the CLIENT asked for, verbatim off
	// the wire. Empty for the overwhelming majority of events, which is why
	// Fingerprint falls back to the derived key rather than treating this as
	// authoritative. See Fingerprint for why honouring it matters.
	ClientFingerprint []string
}

// sentryEvent is the subset of the Sentry event payload Flare reads.
type sentryEvent struct {
	EventID     string          `json:"event_id"`
	Level       string          `json:"level"`
	Platform    string          `json:"platform"`
	Environment string          `json:"environment"`
	Release     string          `json:"release"`
	Transaction string          `json:"transaction"`
	Message     json.RawMessage `json:"message"`
	// Fingerprint is the Sentry-wire grouping override. Typed as a lenient
	// raw message rather than []string because a client that sends it as a
	// bare string (or anything else) must not fail the whole event, which is
	// the same rule the exception and contexts decoders below follow.
	Fingerprint json.RawMessage `json:"fingerprint"`
	Logentry    *struct {
		Formatted string `json:"formatted"`
		Message   string `json:"message"`
	} `json:"logentry"`
	Exception *exceptionField `json:"exception"`
	// Contexts is kept raw and parsed leniently: a client that sends contexts
	// (or contexts.trace) as a non-object must not fail the whole event. Typing
	// it as an object here would reintroduce the drop-on-unexpected-shape bug
	// the exception decoder above exists to avoid.
	Contexts json.RawMessage `json:"contexts"`
}

// parseTraceContext pulls trace_id/span_id from the event's contexts.trace,
// tolerating any shape mismatch by returning empty ids rather than erroring, so
// ingest never drops an event over an unexpected contexts payload.
func parseTraceContext(raw json.RawMessage) (traceID, spanID string) {
	if len(raw) == 0 {
		return "", ""
	}
	var ctx struct {
		Trace *struct {
			TraceID string `json:"trace_id"`
			SpanID  string `json:"span_id"`
		} `json:"trace"`
	}
	if json.Unmarshal(raw, &ctx) != nil || ctx.Trace == nil {
		return "", ""
	}
	return ctx.Trace.TraceID, ctx.Trace.SpanID
}

type exceptionValue struct {
	Type       string `json:"type"`
	Value      string `json:"value"`
	Stacktrace *struct {
		Frames []Frame `json:"frames"`
	} `json:"stacktrace"`
}

// exceptionField accepts BOTH Sentry wire shapes for "exception": the object
// form {"values":[...]} (@sentry/js, sentry-python) and the bare array form
// [...] (sentry-go). Handling only the object form made every sentry-go event
// fail unmarshal, so ingest returned 200 with id "" and dropped the event.
type exceptionField struct {
	Values []exceptionValue `json:"values"`
}

func (e *exceptionField) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		return json.Unmarshal(trimmed, &e.Values)
	}
	type objectForm exceptionField
	var obj objectForm
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return err
	}
	e.Values = obj.Values
	return nil
}

// canonicalLevel folds a client-supplied level to the one spelling the whole
// system compares against, at the trust boundary, once.
//
// The Go alert gate lowercased and trimmed; the SQL ranking CASE lowercased;
// and TEN SQL filter predicates spelled `AND level NOT IN ('info', 'debug')`
// with raw case-sensitive equality. So "INFO", which is what an
// OTLP-conventional service emits, was informational to the code that decides
// whether to page and actionable to every query that counts errors.
//
// That is not hypothetical. CountActionableEventsForProjectSince exists
// BECAUSE "an error-rate anomaly must not fire on heartbeat volume", and its
// own comment records the whole estate tripping that rule on 2026-07-11 when
// the heartbeat shipped. Any service emitting uppercase levels reproduces that
// incident exactly: heartbeats counted as errors against a zero baseline, and
// the anomaly rule pages everyone.
//
// Fixing the ten predicates individually is the wrong shape; the eleventh would
// be written next month. The column only ever holds a canonical level now.
func canonicalLevel(level string) string {
	return firstNonEmpty(strings.ToLower(strings.TrimSpace(level)), "error")
}

// ParseEvent decodes a single event payload and normalizes it.
func ParseEvent(raw []byte) (NormalizedEvent, error) {
	var se sentryEvent
	if err := json.Unmarshal(raw, &se); err != nil {
		return NormalizedEvent{}, err
	}

	ev := NormalizedEvent{
		EventID:     se.EventID,
		Level:       canonicalLevel(se.Level),
		Platform:    se.Platform,
		Environment: se.Environment,
		Release:     se.Release,
		Culprit:     se.Transaction,
		Message:     messageText(se),
		Raw:         json.RawMessage(raw),
	}

	if se.Exception != nil && len(se.Exception.Values) > 0 {
		// Sentry orders values oldest-first; the last is the outermost.
		last := se.Exception.Values[len(se.Exception.Values)-1]
		ev.ExceptionType = last.Type
		ev.ExceptionValue = last.Value
		if last.Stacktrace != nil {
			ev.Frames = last.Stacktrace.Frames
		}
	}

	ev.ClientFingerprint = parseFingerprint(se.Fingerprint)

	ev.TraceID, ev.SpanID = parseTraceContext(se.Contexts)

	ev.Title = title(ev)
	if ev.Culprit == "" {
		ev.Culprit = culprit(ev.Frames)
	}
	return bound(ev), nil
}

// defaultFingerprintToken is Sentry's placeholder for "splice the
// server-computed default in here", so a client can REFINE the default
// grouping (["{{ default }}", tenantID]) instead of replacing it.
const defaultFingerprintToken = "{{ default }}"

// Bounds on the client-supplied grouping key. Same reasoning as the byte caps
// on every other client-controlled column: the fingerprint is attacker-shaped
// input that decides which row an event lands on, so it is capped in BOTH
// dimensions before it reaches the hash.
const (
	maxFingerprintParts     = 32
	maxFingerprintPartBytes = 256
)

// parseFingerprint reads the Sentry-wire fingerprint leniently. A shape it does
// not understand yields nil, which means "no override" and leaves the derived
// grouping untouched, rather than failing the event.
func parseFingerprint(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '[' {
		// A bare string is not the documented shape, but it is an obvious
		// client slip and its intent is unambiguous.
		var one string
		if json.Unmarshal(raw, &one) != nil {
			return nil
		}
		if one = cleanFingerprintPart(one); one == "" {
			return nil
		}
		return []string{one}
	}

	// Decode element by element and STOP at the cap, rather than unmarshalling
	// the array and trimming afterwards. The array is client-controlled and
	// compresses extremely well: a gzipped body of a few KB expands to hundreds
	// of thousands of entries, and materialising them costs hundreds of MB per
	// concurrent request for a field that is truncated to maxFingerprintParts
	// anyway. Streaming bounds the cost at ~maxFingerprintParts *
	// maxFingerprintPartBytes no matter what arrives.
	//
	// The cap is on elements READ, not elements kept, so the work is bounded
	// too: a caller who sends thirty-two empty strings followed by a real one
	// gets no fingerprint. That is the correct trade -- the alternative is
	// letting the client decide how long we spend skipping its padding.
	out := make([]string, 0, maxFingerprintParts)
	for i := 0; i < maxFingerprintParts && dec.More(); i++ {
		var p string
		if err := dec.Decode(&p); err != nil {
			// A non-string element means this is not a fingerprint array at
			// all; fall back to the derived key rather than half-honouring it.
			return nil
		}
		// An empty part carries no grouping information. Keeping it would let
		// a client that sends [""] collapse every event in the project into
		// one issue, which is the failure mode this bound exists to prevent.
		if p = cleanFingerprintPart(p); p == "" {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// cleanFingerprintPart normalises one part and bounds its length. Returns ""
// for anything that carries no grouping information.
func cleanFingerprintPart(p string) string {
	return strings.TrimSpace(truncate(SanitizeText(p), maxFingerprintPartBytes))
}

// Fingerprint is the stable grouping key.
//
// A client-supplied fingerprint WINS when present. It is the only way a caller
// can group events whose message text varies by design, and ignoring it was a
// silent alert-destroying bug rather than a cosmetic one: Hephaestus's
// ci-health watchdog sets a stable fingerprint ("ci-health:hephaestus") and
// emits a message ending in "head-of-queue has waited 1973s", so grouping by
// message text minted a BRAND-NEW unresolved error issue every five minutes.
// It produced 372 issues in ten days, each with event_count 1, which is both
// the alert and the thing that buries the alert.
//
// Absent an override the derived key is unchanged, so this is additive: every
// existing issue keeps its grouping and no backfill is required.
func (e NormalizedEvent) Fingerprint() string {
	parts := e.ClientFingerprint
	if len(parts) == 0 {
		return e.derivedFingerprint()
	}
	// ["{{ default }}"] alone means exactly the default. Hashing the token
	// would silently move those events into a new group, which is the one
	// outcome a caller writing "default" cannot have meant.
	if len(parts) == 1 && parts[0] == defaultFingerprintToken {
		return e.derivedFingerprint()
	}
	h := sha1.New()
	for _, p := range parts {
		if p == defaultFingerprintToken {
			p = e.derivedFingerprint()
		}
		// A separator that cannot occur in the parts themselves, so ["ab","c"]
		// and ["a","bc"] cannot collide.
		io.WriteString(h, "\x1e"+p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// derivedFingerprint is the server-computed grouping key: exceptions group by
// type + in-app frame signatures; bare messages group by their text.
func (e NormalizedEvent) derivedFingerprint() string {
	h := sha1.New()
	if e.ExceptionType != "" {
		io.WriteString(h, e.ExceptionType)
		wrote := false
		for _, f := range e.Frames {
			if f.inApp() && (f.Function != "" || f.Module != "") {
				io.WriteString(h, "\x1f"+f.Module+":"+f.Function)
				wrote = true
			}
		}
		if !wrote {
			io.WriteString(h, "\x1f"+e.ExceptionValue)
		}
	} else {
		io.WriteString(h, strings.TrimSpace(e.Message))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// titleMax bounds every issue title. The exception form used to be unbounded,
// so an 8 MiB exception message became an 8 MiB title that the issues list then
// returned 50 of per request.
const titleMax = 200

// Byte bounds for the rest of the client-supplied text.
//
// The reasoning behind titleMax applies unchanged to every column beside it, and
// for a while it was applied to none of them. `TEXT NOT NULL` in the migration
// is not a bound: Postgres TEXT holds up to 1 GB. Culprit, level and platform
// sit in the same issues row that the list endpoint returns 50 of; message,
// exception_value, release and environment sit on the events row; release is
// also upserted into the releases table and listed in the UI. Each was capped
// only by the 8 MiB body limit, which is a per-request bound, not a per-row one.
//
// The caps are sized to the semantics of each column rather than uniformly, so
// truncation cannot bite real data: a level is an enum word, a platform is a
// language name, a culprit is a module path.
const (
	// maxTextBytes is the universal backstop applied by SanitizeText, so a
	// pillar that never goes near ParseEvent (logs, spans, metrics, and whatever
	// is added next) still cannot write an unbounded column. Deliberately
	// generous: a log body legitimately carries a formatted stack trace.
	maxTextBytes = 16 << 10

	maxMessageBytes        = 8 << 10
	maxExceptionValueBytes = 8 << 10
	maxExceptionTypeBytes  = 256
	maxCulpritBytes        = 500
	maxFrameTextBytes      = 500
	maxReleaseBytes        = 200
	maxEnvironmentBytes    = 64
	maxPlatformBytes       = 64
	maxEventIDBytes        = 64
	maxTraceIDBytes        = 64
	maxLevelBytes          = 32
)

// bound clips every stored string on the event to its column's cap.
//
// Called at the end of ParseEvent so the whole package returns a bounded
// NormalizedEvent by construction, rather than leaving each persist site to
// remember. Frames are included: they are stored in the stacktrace JSONB and
// culprit() copies a module and function straight into the issues row.
//
// This runs BEFORE Fingerprint is ever computed, so grouping is over bounded
// text too. That is a deliberate behaviour change at the pathological end: two
// events whose messages match for the first 8 KiB and diverge after now group
// together, where before they did not. For an error tracker that is the better
// answer, and hashing megabytes per event was itself work one request bought.
func bound(ev NormalizedEvent) NormalizedEvent {
	ev.EventID = truncate(ev.EventID, maxEventIDBytes)
	ev.Level = truncate(ev.Level, maxLevelBytes)
	ev.Platform = truncate(ev.Platform, maxPlatformBytes)
	ev.Environment = truncate(ev.Environment, maxEnvironmentBytes)
	ev.Release = truncate(ev.Release, maxReleaseBytes)
	ev.Title = truncate(ev.Title, titleMax)
	ev.Culprit = truncate(ev.Culprit, maxCulpritBytes)
	ev.ExceptionType = truncate(ev.ExceptionType, maxExceptionTypeBytes)
	ev.ExceptionValue = truncate(ev.ExceptionValue, maxExceptionValueBytes)
	ev.Message = truncate(ev.Message, maxMessageBytes)
	ev.TraceID = truncate(ev.TraceID, maxTraceIDBytes)
	ev.SpanID = truncate(ev.SpanID, maxTraceIDBytes)
	for i := range ev.Frames {
		ev.Frames[i].Filename = truncate(ev.Frames[i].Filename, maxFrameTextBytes)
		ev.Frames[i].Function = truncate(ev.Frames[i].Function, maxFrameTextBytes)
		ev.Frames[i].Module = truncate(ev.Frames[i].Module, maxFrameTextBytes)
		ev.Frames[i].ContextLine = truncate(ev.Frames[i].ContextLine, maxFrameTextBytes)
	}
	return ev
}

func title(e NormalizedEvent) string {
	if e.ExceptionType != "" {
		if e.ExceptionValue != "" {
			return truncate(e.ExceptionType+": "+e.ExceptionValue, titleMax)
		}
		return truncate(e.ExceptionType, titleMax)
	}
	if e.Message != "" {
		return truncate(e.Message, titleMax)
	}
	return "Error"
}

func culprit(frames []Frame) string {
	// Topmost in-app frame, else topmost frame.
	for i := len(frames) - 1; i >= 0; i-- {
		f := frames[i]
		if f.inApp() && (f.Function != "" || f.Module != "") {
			return strings.TrimPrefix(f.Module+"/"+f.Function, "/")
		}
	}
	if len(frames) > 0 {
		f := frames[len(frames)-1]
		return strings.TrimPrefix(f.Module+"/"+f.Function, "/")
	}
	return ""
}

func messageText(se sentryEvent) string {
	if se.Logentry != nil {
		if se.Logentry.Formatted != "" {
			return se.Logentry.Formatted
		}
		if se.Logentry.Message != "" {
			return se.Logentry.Message
		}
	}
	if len(se.Message) == 0 {
		return ""
	}
	// message may be a bare string or {message, formatted}.
	var s string
	if json.Unmarshal(se.Message, &s) == nil {
		return s
	}
	var obj struct {
		Formatted string `json:"formatted"`
		Message   string `json:"message"`
	}
	if json.Unmarshal(se.Message, &obj) == nil {
		return firstNonEmpty(obj.Formatted, obj.Message)
	}
	return ""
}

// Telemetry timestamps are fully client-controlled (an SDK, an OTLP collector,
// or anyone holding a DSN). Anything outside this window has no dated partition
// and lands in the table's DEFAULT partition, which retention never prunes and
// the cold exporter never archives; worse, a row in DEFAULT permanently blocks
// CREATE TABLE ... PARTITION OF for that day, so one bogus timestamp can wedge
// partition creation for good. The bounds are kept strictly inside what the
// partition manager pre-creates (BackfillDays back, aheadDays forward).
const (
	// MaxBackfillDays bounds how far in the past a client timestamp is honored.
	MaxBackfillDays = 7
	// MaxAhead bounds clock skew into the future. The manager pre-creates 3 days
	// ahead, so 48h leaves a full day of margin.
	MaxAhead = 48 * time.Hour
)

// SanitizeText makes a client-supplied string safe for a Postgres TEXT column.
// Postgres rejects BOTH a NUL byte ("invalid byte sequence"/"unsupported Unicode
// escape") and invalid UTF-8, and logs/spans/metrics are written with CopyFrom,
// which is all-or-nothing: one bad byte in one record failed the ENTIRE batch.
// With an OTLP collector retrying that batch forever, a single poisoned record
// blocked the pipeline permanently.
func SanitizeText(s string) string {
	if s == "" {
		return s
	}
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	return s
}

// SanitizeColumn is SanitizeText plus the universal length backstop, for text on
// its way into a TEXT COLUMN.
//
// Every pillar meets here: persistLogs, persistSpans and persistMetrics write
// their columns through it, and ParseEvent applies tighter per-column caps on
// top for the events path. That makes this the one place a bound covers a pillar
// added later by someone who never reads this file.
//
// It is deliberately NOT folded into SanitizeText, because SanitizeText also
// runs over every string leaf and key inside the payload JSONB. That document is
// what an operator opens to see the request body and breadcrumbs behind an
// error, and quietly clipping its contents would damage the one job the product
// has. A column and a document want different answers.
//
// Truncation runs last so it cuts a valid string rather than manufacturing the
// invalid UTF-8 SanitizeText just removed.
func SanitizeColumn(s string) string {
	return truncate(SanitizeText(s), maxTextBytes)
}

// SanitizeJSON makes a client-supplied JSON document safe for a JSONB column.
//
// It PARSES and re-marshals rather than rewriting the text. A text-level
// replace of the six-character \u0000 escape is wrong: in JSON that escape can
// itself be escaped, so {"k":"\\u0000"} is a valid document whose value is the
// literal text \u0000, and stripping those six characters leaves {"k":"\\"},
// which is invalid, recreating exactly the CopyFrom poison pill this exists to
// remove. Parsing also fixes lone surrogate escapes (\ud800 with no pair),
// which Postgres rejects too and which no text replace would catch: the decoder
// turns them into U+FFFD.
func SanitizeJSON(raw []byte) []byte {
	if len(raw) == 0 {
		return raw
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	// Keep numbers as their literal text so a large int64 (an id, a nanosecond
	// timestamp) is not silently rounded through float64.
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		// Not valid JSON to begin with. Preserve the payload as a sanitized
		// string rather than dropping it or failing the whole batch.
		if out, merr := marshalJSON(map[string]string{"_raw": SanitizeText(string(raw))}); merr == nil {
			return out
		}
		return []byte(`{"_raw":"unserializable"}`)
	}
	out, err := marshalJSON(sanitizeValue(v))
	if err != nil {
		return []byte(`{"_sanitize_error":true}`)
	}
	return out
}

// sanitizeValue strips NULs and invalid UTF-8 from every string leaf and object
// key. Keys are included because a NUL in a key fails the insert just as a NUL
// in a value does; unlike the PII scrubber this rewrite is near-identity, so it
// cannot meaningfully collapse two distinct keys into one.
func sanitizeValue(v any) any {
	switch t := v.(type) {
	case string:
		return SanitizeText(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[SanitizeText(k)] = sanitizeValue(val)
		}
		return out
	case []any:
		for i := range t {
			t[i] = sanitizeValue(t[i])
		}
		return t
	default:
		return v
	}
}

// marshalJSON encodes without HTML-escaping, so <, > and & in a log body or a
// span attribute survive the round trip unchanged.
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// ClampTime keeps a client-supplied timestamp inside the partitioned window.
// A zero or out-of-range value is replaced with now, which is what Sentry and
// the OTLP collectors do; the alternative is silently unqueryable data.
func ClampTime(t, now time.Time) time.Time {
	if t.IsZero() {
		return now
	}
	if t.Before(now.AddDate(0, 0, -MaxBackfillDays)) || t.After(now.Add(MaxAhead)) {
		return now
	}
	return t
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// truncate cuts s to at most n bytes WITHOUT splitting a UTF-8 rune. Slicing on
// a raw byte boundary produced invalid UTF-8, which Postgres rejects on insert
// ("invalid byte sequence for encoding UTF8"), silently dropping every event
// whose text straddled the limit with a multi-byte character.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
