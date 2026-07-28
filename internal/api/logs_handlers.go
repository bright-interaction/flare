package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bright-interaction/flare/internal/ai"
	"github.com/bright-interaction/flare/internal/db/generated"
	"github.com/bright-interaction/flare/internal/id"
	"github.com/bright-interaction/flare/internal/ingest"
	"github.com/bright-interaction/flare/internal/telemetry"
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
			ID:        id.New(),
			ProjectID: project.ID,
			OrgID:     project.OrgID,
			Severity:  ingest.SanitizeText(rec.Severity),
			// InsertLogs is a CopyFrom: it is all-or-nothing, so one record
			// carrying a NUL byte or invalid UTF-8 failed the whole batch, and an
			// OTLP collector's retry made that record a permanent poison pill.
			Body:       ingest.SanitizeText(rec.Body),
			Attributes: ingest.SanitizeJSON(rec.Attributes),
			TraceID:    ingest.SanitizeText(rec.TraceID),
			SpanID:     ingest.SanitizeText(rec.SpanID),
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

const (
	// maxLogWindowHours clamps `hours`. 90 days is past any retention setting
	// this ships with, so a larger value only asks the database to prove the
	// rows are absent.
	maxLogWindowHours = 24 * 90
	// maxLogLimit caps a page. 100 is the default; 500 is enough for an export
	// loop without letting one request pull an unbounded slice into memory.
	maxLogLimit = 500
)

// handleSearchLogs is the authenticated dashboard query over the hot tier.
func (s *Server) handleSearchLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := telemetry.LogFilter{Limit: 100}
	if v := strings.TrimSpace(q.Get("q")); v != "" {
		f.Query = &v
	}
	if v := strings.TrimSpace(q.Get("severity")); v != "" {
		f.Severity = &v
	}
	if v := strings.TrimSpace(q.Get("trace_id")); v != "" {
		f.TraceID = &v
	}
	// Time window. `hours` is what the UI sends; `since` (RFC3339) is the
	// precise form for API and MCP callers. Without either, the query scanned
	// the entire retained history and returned the newest 100, so an operator
	// looking at an incident from Tuesday had no way to ask for Tuesday.
	if v := strings.TrimSpace(q.Get("since")); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.Since = &t
		} else {
			writeErr(w, http.StatusBadRequest, "since must be an RFC3339 timestamp")
			return
		}
	} else if v := strings.TrimSpace(q.Get("hours")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeErr(w, http.StatusBadRequest, "hours must be a positive integer")
			return
		}
		// Clamped: a huge value is the same unbounded scan the window exists to
		// prevent, and nothing is retained beyond it anyway.
		if n > maxLogWindowHours {
			n = maxLogWindowHours
		}
		t := time.Now().Add(-time.Duration(n) * time.Hour)
		f.Since = &t
	}
	// Keyset paging cursor: the previous page's oldest row.
	if v := strings.TrimSpace(q.Get("before")); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "before must be an RFC3339 timestamp")
			return
		}
		f.Before = &t
	}
	if v := strings.TrimSpace(q.Get("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeErr(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		if n > maxLogLimit {
			n = maxLogLimit
		}
		f.Limit = int32(n)
	}

	logs, err := s.store.SearchLogs(r.Context(), chi.URLParam(r, "id"), orgIDFrom(r.Context()), f)
	if err != nil {
		slogError(w, "search logs", err)
		return
	}
	out := toLogResponses(logs)
	// next_before is the cursor for the following page, present only when this
	// page was full. An empty value means "no more", which is what lets the UI
	// distinguish the end of the data from a page that happens to be short.
	resp := map[string]any{"logs": out}
	if int32(len(logs)) == f.Limit && len(logs) > 0 {
		resp["next_before"] = logs[len(logs)-1].ObservedAt.Format(time.RFC3339Nano)
	}
	writeJSON(w, http.StatusOK, resp)
}

// toLogResponses maps stored logs to the clean API shape. Used by the REST
// dashboard, where a human views their own in-region logs (not an egress
// boundary), so bodies + attributes are returned as-is.
func toLogResponses(logs []telemetry.Log) []logResponse {
	out := make([]logResponse, 0, len(logs))
	for _, l := range logs {
		out = append(out, logResponse{
			ID: l.ID, Severity: l.Severity, Body: l.Body, Attributes: l.Attributes,
			TraceID: l.TraceID, SpanID: l.SpanID, ObservedAt: l.ObservedAt,
		})
	}
	return out
}

// toLogResponsesScrubbed is the MCP-egress variant: it runs each log body +
// attributes through ai.Scrub before the rows leave for an LLM. The logs pillar
// has no per-log sensitive flag (unlike issues), and bodies routinely carry PII
// (emails, tokens, ids), so search_logs must scrub on the way out - matching how
// get_issue scrubs sensitive event messages.
func toLogResponsesScrubbed(logs []telemetry.Log) []logResponse {
	out := toLogResponses(logs)
	for i := range out {
		out[i].Body = ai.Scrub(out[i].Body)
		if len(out[i].Attributes) > 0 {
			out[i].Attributes = json.RawMessage(ai.ScrubJSON(out[i].Attributes))
		}
	}
	return out
}
