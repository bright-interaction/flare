package ingest

import (
	"encoding/json"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// MetricRecord is Flare's normalized metric datapoint, mapped from OTLP or the
// native API. One row per (metric name, datapoint).
type MetricRecord struct {
	Name       string
	Kind       string // gauge | sum | histogram
	Value      float64
	Labels     json.RawMessage
	ObservedAt time.Time
}

// ParseOTLPMetrics decodes an OTLP ExportMetricsServiceRequest (protobuf or,
// when asJSON, OTLP/JSON) and flattens it to metric datapoints. Gauge and Sum
// carry number datapoints directly; a Histogram is summarized as two points,
// <name>_sum and <name>_count, which covers latency/size histograms without the
// full bucket model.
func ParseOTLPMetrics(body []byte, asJSON bool) ([]MetricRecord, error) {
	var req colmetricspb.ExportMetricsServiceRequest
	var err error
	if asJSON {
		err = protojson.Unmarshal(body, &req)
	} else {
		err = proto.Unmarshal(body, &req)
	}
	if err != nil {
		return nil, err
	}

	var out []MetricRecord
	for _, rm := range req.GetResourceMetrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				out = append(out, normalizeMetric(m)...)
			}
		}
	}
	return out, nil
}

func normalizeMetric(m *metricspb.Metric) []MetricRecord {
	name := m.GetName()
	if name == "" {
		return nil // drop nameless metrics, matching the native path
	}
	var out []MetricRecord
	switch data := m.GetData().(type) {
	case *metricspb.Metric_Gauge:
		for _, dp := range data.Gauge.GetDataPoints() {
			out = append(out, numberPoint(name, "gauge", dp))
		}
	case *metricspb.Metric_Sum:
		for _, dp := range data.Sum.GetDataPoints() {
			out = append(out, numberPoint(name, "sum", dp))
		}
	case *metricspb.Metric_Histogram:
		for _, dp := range data.Histogram.GetDataPoints() {
			at := unixNano(dp.GetTimeUnixNano())
			labels := kvToJSON(dp.GetAttributes())
			out = append(out,
				MetricRecord{Name: name + "_sum", Kind: "histogram", Value: dp.GetSum(), Labels: labels, ObservedAt: at},
				MetricRecord{Name: name + "_count", Kind: "histogram", Value: float64(dp.GetCount()), Labels: labels, ObservedAt: at},
			)
		}
	}
	return out
}

func numberPoint(name, kind string, dp *metricspb.NumberDataPoint) MetricRecord {
	return MetricRecord{
		Name:       name,
		Kind:       kind,
		Value:      numberValue(dp),
		Labels:     kvToJSON(dp.GetAttributes()),
		ObservedAt: unixNano(dp.GetTimeUnixNano()),
	}
}

func numberValue(dp *metricspb.NumberDataPoint) float64 {
	switch v := dp.GetValue().(type) {
	case *metricspb.NumberDataPoint_AsDouble:
		return v.AsDouble
	case *metricspb.NumberDataPoint_AsInt:
		return float64(v.AsInt)
	}
	return 0
}

func unixNano(ts uint64) time.Time {
	now := time.Now().UTC()
	if ts == 0 {
		return now
	}
	// int64 overflow on a huge uint64 yields a negative nanosecond offset (a
	// year-1754 timestamp); ClampTime catches that along with any out-of-window
	// value, so nothing reaches the DEFAULT partition.
	return ClampTime(time.Unix(0, int64(ts)).UTC(), now)
}

type nativeMetric struct {
	Name      string          `json:"name"`
	Kind      string          `json:"kind"`
	Value     float64         `json:"value"`
	Labels    json.RawMessage `json:"labels"`
	Timestamp string          `json:"timestamp"`
}

// ParseNativeMetrics accepts a bare JSON array of metric points or
// {"metrics":[...]}.
func ParseNativeMetrics(body []byte) ([]MetricRecord, error) {
	var arr []nativeMetric
	if err := json.Unmarshal(body, &arr); err != nil {
		var wrap struct {
			Metrics []nativeMetric `json:"metrics"`
		}
		if err2 := json.Unmarshal(body, &wrap); err2 != nil {
			return nil, err
		}
		arr = wrap.Metrics
	}

	out := make([]MetricRecord, 0, len(arr))
	for _, m := range arr {
		if m.Name == "" {
			continue
		}
		now := time.Now().UTC()
		when := now
		if m.Timestamp != "" {
			if t, err := time.Parse(time.RFC3339, m.Timestamp); err == nil {
				when = ClampTime(t, now)
			}
		}
		kind := m.Kind
		if kind == "" {
			kind = "gauge"
		}
		out = append(out, MetricRecord{
			Name: m.Name, Kind: kind, Value: m.Value, Labels: m.Labels, ObservedAt: when,
		})
	}
	return out, nil
}
