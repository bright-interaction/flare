package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bright-interaction/flare/internal/db/generated"
	"github.com/bright-interaction/flare/internal/ingest"
)

// handleOTLPTraces ingests OTLP/HTTP traces (protobuf or OTLP/JSON).
func (s *Server) handleOTLPTraces(w http.ResponseWriter, r *http.Request) {
	project, ok := s.authIngest(w, r)
	if !ok {
		return
	}
	body, ok := readIngestBody(w, r)
	if !ok {
		return
	}
	asJSON := strings.Contains(r.Header.Get("Content-Type"), "json")
	spans, err := ingest.ParseOTLPTraces(body, asJSON)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid OTLP traces payload")
		return
	}
	if err := s.persistSpans(r.Context(), project, spans); err != nil {
		slogError(w, "persist spans", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) persistSpans(ctx context.Context, project *generated.Project, spans []ingest.SpanRecord) error {
	if len(spans) == 0 {
		return nil
	}
	params := make([]generated.InsertSpansParams, 0, len(spans))
	for _, sp := range spans {
		params = append(params, generated.InsertSpansParams{
			TraceID:      sp.TraceID,
			SpanID:       sp.SpanID,
			ParentSpanID: sp.ParentSpanID,
			ProjectID:    project.ID,
			OrgID:        project.OrgID,
			Name:         sp.Name,
			Kind:         sp.Kind,
			Status:       sp.Status,
			StartTime:    pgtype.Timestamptz{Time: sp.Start, Valid: true},
			EndTime:      pgtype.Timestamptz{Time: sp.End, Valid: true},
			DurationMs:   sp.DurationMs,
			Attributes:   sp.Attributes,
		})
	}
	_, err := s.q.InsertSpans(ctx, params)
	return err
}

type traceSummary struct {
	TraceID   string    `json:"trace_id"`
	RootName  string    `json:"root_name"`
	SpanCount int64     `json:"span_count"`
	HasError  bool      `json:"has_error"`
	DurationMs float64  `json:"duration_ms"`
	Started   time.Time `json:"started"`
}

func (s *Server) handleListTraces(w http.ResponseWriter, r *http.Request) {
	traces, err := s.store.ListTraces(r.Context(), chi.URLParam(r, "id"), orgIDFrom(r.Context()), 100)
	if err != nil {
		slogError(w, "list traces", err)
		return
	}
	out := make([]traceSummary, 0, len(traces))
	for _, t := range traces {
		out = append(out, traceSummary{
			TraceID: t.TraceID, RootName: t.RootName, SpanCount: t.SpanCount,
			HasError: t.HasError, DurationMs: t.DurationMs, Started: t.Started,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

type spanResponse struct {
	SpanID       string          `json:"span_id"`
	ParentSpanID string          `json:"parent_span_id"`
	Name         string          `json:"name"`
	Kind         string          `json:"kind"`
	Status       string          `json:"status"`
	StartUnixMs  int64           `json:"start_unix_ms"`
	DurationMs   float64         `json:"duration_ms"`
	Attributes   json.RawMessage `json:"attributes"`
}

func (s *Server) handleGetTrace(w http.ResponseWriter, r *http.Request) {
	spans, err := s.store.GetTraceSpans(r.Context(), chi.URLParam(r, "id"), orgIDFrom(r.Context()))
	if err != nil {
		slogError(w, "get trace", err)
		return
	}
	if len(spans) == 0 {
		writeErr(w, http.StatusNotFound, "trace not found")
		return
	}
	out := make([]spanResponse, 0, len(spans))
	for _, sp := range spans {
		out = append(out, spanResponse{
			SpanID: sp.SpanID, ParentSpanID: sp.ParentSpanID, Name: sp.Name,
			Kind: sp.Kind, Status: sp.Status,
			StartUnixMs: sp.StartUnixMs, DurationMs: sp.DurationMs,
			Attributes: sp.Attributes,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
