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
	"github.com/bright-interaction/flare/internal/id"
	"github.com/bright-interaction/flare/internal/ingest"
)

// handleOTLPLogs ingests OTLP/HTTP logs (protobuf or OTLP/JSON). Auth is the
// DSN public key via X-Flare-Key (OTEL_EXPORTER_OTLP_HEADERS) or sentry_key.
func (s *Server) handleOTLPLogs(w http.ResponseWriter, r *http.Request) {
	project, ok := s.authIngest(w, r)
	if !ok {
		return
	}
	body, ok := readIngestBody(w, r)
	if !ok {
		return
	}
	asJSON := strings.Contains(r.Header.Get("Content-Type"), "json")
	records, err := ingest.ParseOTLPLogs(body, asJSON)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid OTLP logs payload")
		return
	}
	if err := s.persistLogs(r.Context(), project, records); err != nil {
		slogError(w, "persist otlp logs", err)
		return
	}
	// Minimal OTLP success response.
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleNativeLogs ingests a JSON array of log records (the thin SDK path).
func (s *Server) handleNativeLogs(w http.ResponseWriter, r *http.Request) {
	project, ok := s.authIngest(w, r)
	if !ok {
		return
	}
	body, ok := readIngestBody(w, r)
	if !ok {
		return
	}
	records, err := ingest.ParseNativeLogs(body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid logs payload")
		return
	}
	if err := s.persistLogs(r.Context(), project, records); err != nil {
		slogError(w, "persist logs", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"accepted": len(records)})
}

func (s *Server) persistLogs(ctx context.Context, project *generated.Project, records []ingest.LogRecord) error {
	if len(records) == 0 {
		return nil
	}
	params := make([]generated.InsertLogsParams, 0, len(records))
	for _, rec := range records {
		params = append(params, generated.InsertLogsParams{
			ID:         id.New(),
			ProjectID:  project.ID,
			OrgID:      project.OrgID,
			Severity:   rec.Severity,
			Body:       rec.Body,
			Attributes: rec.Attributes,
			TraceID:    rec.TraceID,
			SpanID:     rec.SpanID,
			ObservedAt: pgtype.Timestamptz{Time: rec.ObservedAt, Valid: true},
		})
	}
	_, err := s.q.InsertLogs(ctx, params)
	return err
}

type logResponse struct {
	ID         string          `json:"id"`
	Severity   string          `json:"severity"`
	Body       string          `json:"body"`
	Attributes json.RawMessage `json:"attributes"`
	TraceID    string          `json:"trace_id"`
	SpanID     string          `json:"span_id"`
	ObservedAt time.Time       `json:"observed_at"`
}

// handleSearchLogs is the authenticated dashboard query over the hot tier.
func (s *Server) handleSearchLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	params := generated.SearchLogsParams{
		ProjectID: chi.URLParam(r, "id"),
		OrgID:     orgIDFrom(r.Context()),
		Limit:     100,
	}
	if v := strings.TrimSpace(q.Get("q")); v != "" {
		params.Q = pgText(v)
	}
	if v := strings.TrimSpace(q.Get("severity")); v != "" {
		params.Severity = pgText(v)
	}
	if v := strings.TrimSpace(q.Get("trace_id")); v != "" {
		params.TraceID = pgText(v)
	}

	logs, err := s.q.SearchLogs(r.Context(), params)
	if err != nil {
		slogError(w, "search logs", err)
		return
	}
	out := make([]logResponse, 0, len(logs))
	for _, l := range logs {
		out = append(out, logResponse{
			ID: l.ID, Severity: l.Severity, Body: l.Body, Attributes: l.Attributes,
			TraceID: l.TraceID, SpanID: l.SpanID, ObservedAt: l.ObservedAt.Time,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
