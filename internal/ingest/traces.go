package ingest

import (
	"encoding/json"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// SpanRecord is Flare's normalized span row, mapped from OTLP.
type SpanRecord struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	Name         string
	Kind         string
	Status       string
	Start        time.Time
	End          time.Time
	DurationMs   float64
	Attributes   json.RawMessage
}

// ParseOTLPTraces decodes an OTLP ExportTraceServiceRequest (protobuf or, when
// asJSON, OTLP/JSON) and flattens it to span records.
func ParseOTLPTraces(body []byte, asJSON bool) ([]SpanRecord, error) {
	var req coltracepb.ExportTraceServiceRequest
	var err error
	if asJSON {
		err = protojson.Unmarshal(body, &req)
	} else {
		err = proto.Unmarshal(body, &req)
	}
	if err != nil {
		return nil, err
	}

	var out []SpanRecord
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				out = append(out, normalizeSpan(sp))
			}
		}
	}
	return out, nil
}

func normalizeSpan(sp *tracepb.Span) SpanRecord {
	start := time.Unix(0, int64(sp.GetStartTimeUnixNano())).UTC()
	end := time.Unix(0, int64(sp.GetEndTimeUnixNano())).UTC()
	durMs := 0.0
	if end.After(start) {
		durMs = float64(end.Sub(start).Microseconds()) / 1000.0
	}
	return SpanRecord{
		TraceID:      hexOrEmpty(sp.GetTraceId()),
		SpanID:       hexOrEmpty(sp.GetSpanId()),
		ParentSpanID: hexOrEmpty(sp.GetParentSpanId()),
		Name:         sp.GetName(),
		Kind:         spanKind(sp.GetKind()),
		Status:       spanStatus(sp.GetStatus()),
		Start:        start,
		End:          end,
		DurationMs:   durMs,
		Attributes:   kvToJSON(sp.GetAttributes()),
	}
}

func spanKind(k tracepb.Span_SpanKind) string {
	return strings.ToLower(strings.TrimPrefix(k.String(), "SPAN_KIND_"))
}

func spanStatus(st *tracepb.Status) string {
	if st == nil {
		return "unset"
	}
	return strings.ToLower(strings.TrimPrefix(st.GetCode().String(), "STATUS_CODE_"))
}
