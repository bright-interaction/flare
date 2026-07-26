package ingest

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// LogRecord is Flare's normalized log row, mapped from OTLP or the native API.
type LogRecord struct {
	Severity   string
	Body       string
	Attributes json.RawMessage
	TraceID    string
	SpanID     string
	ObservedAt time.Time
}

// ParseOTLPLogs decodes an OTLP ExportLogsServiceRequest (protobuf or, when
// asJSON, OTLP/JSON) and flattens it to log records.
func ParseOTLPLogs(body []byte, asJSON bool) ([]LogRecord, error) {
	var req collogspb.ExportLogsServiceRequest
	var err error
	if asJSON {
		err = protojson.Unmarshal(body, &req)
	} else {
		err = proto.Unmarshal(body, &req)
	}
	if err != nil {
		return nil, err
	}

	var out []LogRecord
	for _, rl := range req.GetResourceLogs() {
		for _, sl := range rl.GetScopeLogs() {
			for _, lr := range sl.GetLogRecords() {
				out = append(out, normalizeOTLPLog(lr))
			}
		}
	}
	return out, nil
}

func normalizeOTLPLog(lr *logspb.LogRecord) LogRecord {
	sev := lr.GetSeverityText()
	if sev == "" {
		sev = severityFromNumber(lr.GetSeverityNumber())
	}
	ts := lr.GetTimeUnixNano()
	if ts == 0 {
		ts = lr.GetObservedTimeUnixNano()
	}
	now := time.Now().UTC()
	when := now
	if ts != 0 {
		when = ClampTime(time.Unix(0, int64(ts)).UTC(), now)
	}
	return LogRecord{
		Severity:   strings.ToLower(sev),
		Body:       anyValueString(lr.GetBody()),
		Attributes: kvToJSON(lr.GetAttributes()),
		TraceID:    hexOrEmpty(lr.GetTraceId()),
		SpanID:     hexOrEmpty(lr.GetSpanId()),
		ObservedAt: when,
	}
}

type nativeLog struct {
	Severity   string          `json:"severity"`
	Level      string          `json:"level"`
	Body       string          `json:"body"`
	Message    string          `json:"message"`
	Attributes json.RawMessage `json:"attributes"`
	TraceID    string          `json:"trace_id"`
	SpanID     string          `json:"span_id"`
	Timestamp  string          `json:"timestamp"`
}

// ParseNativeLogs accepts a bare JSON array of log records or {"logs":[...]}.
func ParseNativeLogs(body []byte) ([]LogRecord, error) {
	var arr []nativeLog
	if err := json.Unmarshal(body, &arr); err != nil {
		var wrap struct {
			Logs []nativeLog `json:"logs"`
		}
		if err2 := json.Unmarshal(body, &wrap); err2 != nil {
			return nil, err
		}
		arr = wrap.Logs
	}

	out := make([]LogRecord, 0, len(arr))
	for _, l := range arr {
		now := time.Now().UTC()
		when := now
		if l.Timestamp != "" {
			if t, err := time.Parse(time.RFC3339, l.Timestamp); err == nil {
				when = ClampTime(t, now)
			}
		}
		out = append(out, LogRecord{
			Severity:   strings.ToLower(firstNonEmpty(l.Severity, l.Level, "info")),
			Body:       firstNonEmpty(l.Body, l.Message),
			Attributes: l.Attributes,
			TraceID:    l.TraceID,
			SpanID:     l.SpanID,
			ObservedAt: when,
		})
	}
	return out, nil
}

func severityFromNumber(n logspb.SeverityNumber) string {
	switch {
	case n >= 21:
		return "fatal"
	case n >= 17:
		return "error"
	case n >= 13:
		return "warn"
	case n >= 9:
		return "info"
	case n >= 5:
		return "debug"
	case n >= 1:
		return "trace"
	default:
		return "info"
	}
}

func hexOrEmpty(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return hex.EncodeToString(b)
}

func anyValueString(v *commonpb.AnyValue) string {
	if v == nil {
		return ""
	}
	if s, ok := v.GetValue().(*commonpb.AnyValue_StringValue); ok {
		return s.StringValue
	}
	b, err := json.Marshal(anyValueGo(v))
	if err != nil {
		return ""
	}
	return string(b)
}

func anyValueGo(v *commonpb.AnyValue) any {
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return x.StringValue
	case *commonpb.AnyValue_BoolValue:
		return x.BoolValue
	case *commonpb.AnyValue_IntValue:
		return x.IntValue
	case *commonpb.AnyValue_DoubleValue:
		return x.DoubleValue
	case *commonpb.AnyValue_BytesValue:
		return base64.StdEncoding.EncodeToString(x.BytesValue)
	case *commonpb.AnyValue_ArrayValue:
		arr := make([]any, 0, len(x.ArrayValue.GetValues()))
		for _, e := range x.ArrayValue.GetValues() {
			arr = append(arr, anyValueGo(e))
		}
		return arr
	case *commonpb.AnyValue_KvlistValue:
		m := map[string]any{}
		for _, kv := range x.KvlistValue.GetValues() {
			m[kv.GetKey()] = anyValueGo(kv.GetValue())
		}
		return m
	default:
		return nil
	}
}

func kvToJSON(kvs []*commonpb.KeyValue) json.RawMessage {
	if len(kvs) == 0 {
		return nil
	}
	m := make(map[string]any, len(kvs))
	for _, kv := range kvs {
		m[kv.GetKey()] = anyValueGo(kv.GetValue())
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return b
}
