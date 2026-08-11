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

// ParseEvent decodes a single event payload and normalizes it.
func ParseEvent(raw []byte) (NormalizedEvent, error) {
	var se sentryEvent
	if err := json.Unmarshal(raw, &se); err != nil {
		return NormalizedEvent{}, err
	}

	ev := NormalizedEvent{
		EventID:     se.EventID,
		Level:       firstNonEmpty(se.Level, "error"),
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

	ev.TraceID, ev.SpanID = parseTraceContext(se.Contexts)

	ev.Title = title(ev)
	if ev.Culprit == "" {
		ev.Culprit = culprit(ev.Frames)
	}
	return bound(ev), nil
}

// Fingerprint is the stable grouping key. Exceptions group by type + in-app
// frame signatures; bare messages group by their text.
func (e NormalizedEvent) Fingerprint() string {
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
