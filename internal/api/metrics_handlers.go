package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bright-interaction/flare/internal/db/generated"
	"github.com/bright-interaction/flare/internal/id"
	"github.com/bright-interaction/flare/internal/ingest"
)

// handleOTLPMetrics ingests OTLP/HTTP metrics (protobuf or OTLP/JSON).
func (s *Server) handleOTLPMetrics(w http.ResponseWriter, r *http.Request) {
	project, ok := s.authIngest(w, r)
	if !ok {
		return
	}
	body, ok := readIngestBody(w, r)
	if !ok {
		return
	}
	asJSON := strings.Contains(r.Header.Get("Content-Type"), "json")
	records, err := ingest.ParseOTLPMetrics(body, asJSON)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid OTLP metrics payload")
		return
	}
	if err := s.persistMetrics(r.Context(), project, records); err != nil {
		slogError(w, "persist metrics", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleNativeMetrics ingests a bare JSON array of metric points (or {"metrics":[...]}).
func (s *Server) handleNativeMetrics(w http.ResponseWriter, r *http.Request) {
	project, ok := s.authIngest(w, r)
	if !ok {
		return
	}
	body, ok := readIngestBody(w, r)
	if !ok {
		return
	}
	records, err := ingest.ParseNativeMetrics(body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid metrics payload")
		return
	}
	if err := s.persistMetrics(r.Context(), project, records); err != nil {
		slogError(w, "persist metrics", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) persistMetrics(ctx context.Context, project *generated.Project, records []ingest.MetricRecord) error {
	if len(records) == 0 {
		return nil
	}
	params := make([]generated.InsertMetricsParams, 0, len(records))
	for _, m := range records {
		// NaN and +/-Inf are accepted by float64 and by Postgres double
		// precision, but they are not representable in JSON: the query endpoint
		// and the MCP tool then failed to marshal and answered 200 with an empty
		// body. Drop them at the door instead.
		if math.IsNaN(m.Value) || math.IsInf(m.Value, 0) {
			continue
		}
		params = append(params, generated.InsertMetricsParams{
			ID:         id.New(),
			ProjectID:  project.ID,
			OrgID:      project.OrgID,
			Name:       ingest.SanitizeText(m.Name),
			Kind:       ingest.SanitizeText(m.Kind),
			Value:      m.Value,
			Labels:     ingest.SanitizeJSON(m.Labels),
			ObservedAt: pgtype.Timestamptz{Time: m.ObservedAt, Valid: true},
		})
	}
	if len(params) == 0 {
		return nil
	}
	_, err := s.q.InsertMetrics(ctx, params)
	return err
}

type metricNameResponse struct {
	Name     string    `json:"name"`
	Kind     string    `json:"kind"`
	Points   int64     `json:"points"`
	LastSeen time.Time `json:"last_seen"`
}

func (s *Server) handleListMetrics(w http.ResponseWriter, r *http.Request) {
	proj, err := s.q.GetProjectByID(r.Context(), generated.GetProjectByIDParams{
		ID: chi.URLParam(r, "id"), OrgScope: orgIDFrom(r.Context()),
	})
	if err != nil {
		writeErr(w, http.StatusNotFound, "project not found")
		return
	}
	names, err := s.store.ListMetricNames(r.Context(), proj.ID, proj.OrgID)
	if err != nil {
		slogError(w, "list metrics", err)
		return
	}
	out := make([]metricNameResponse, 0, len(names))
	for _, m := range names {
		out = append(out, metricNameResponse{Name: m.Name, Kind: m.Kind, Points: m.Points, LastSeen: m.LastSeen})
	}
	writeJSON(w, http.StatusOK, out)
}

type metricPointResponse struct {
	Value      float64         `json:"value"`
	Labels     json.RawMessage `json:"labels"`
	ObservedAt time.Time       `json:"observed_at"`
}

// handleQueryMetric returns the raw points of one metric over a window
// (?name=, ?window= minutes, default 60), oldest first for charting.
func (s *Server) handleQueryMetric(w http.ResponseWriter, r *http.Request) {
	proj, err := s.q.GetProjectByID(r.Context(), generated.GetProjectByIDParams{
		ID: chi.URLParam(r, "id"), OrgScope: orgIDFrom(r.Context()),
	})
	if err != nil {
		writeErr(w, http.StatusNotFound, "project not found")
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	windowMin := 60
	if v, err := strconv.Atoi(r.URL.Query().Get("window")); err == nil && v > 0 && v <= 10080 {
		windowMin = v
	}
	since := time.Now().Add(-time.Duration(windowMin) * time.Minute)
	points, err := s.store.QueryMetricSeries(r.Context(), proj.ID, proj.OrgID, name, since, 1000)
	if err != nil {
		slogError(w, "query metric", err)
		return
	}
	// The query returns newest-first; reverse to oldest-first for the chart.
	out := make([]metricPointResponse, 0, len(points))
	for i := len(points) - 1; i >= 0; i-- {
		p := points[i]
		out = append(out, metricPointResponse{Value: p.Value, Labels: p.Labels, ObservedAt: p.ObservedAt})
	}
	writeJSON(w, http.StatusOK, out)
}
