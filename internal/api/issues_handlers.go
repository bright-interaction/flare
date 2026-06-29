package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bright-interaction/flare/internal/db/generated"
)

type issueResponse struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	Culprit    string    `json:"culprit"`
	Level      string    `json:"level"`
	Status     string    `json:"status"`
	Platform   string    `json:"platform"`
	FirstSeen  time.Time `json:"first_seen"`
	LastSeen   time.Time `json:"last_seen"`
	EventCount int64     `json:"event_count"`
}

func toIssueResponse(i *generated.Issue) issueResponse {
	return issueResponse{
		ID: i.ID, Title: i.Title, Culprit: i.Culprit, Level: i.Level,
		Status: i.Status, Platform: i.Platform,
		FirstSeen: i.FirstSeen.Time, LastSeen: i.LastSeen.Time, EventCount: i.EventCount,
	}
}

type eventResponse struct {
	ID             string          `json:"id"`
	Level          string          `json:"level"`
	Message        string          `json:"message"`
	ExceptionType  string          `json:"exception_type"`
	ExceptionValue string          `json:"exception_value"`
	Platform       string          `json:"platform"`
	Environment    string          `json:"environment"`
	Release        string          `json:"release"`
	Stacktrace     json.RawMessage `json:"stacktrace"`
	ReceivedAt     time.Time       `json:"received_at"`
}

func (s *Server) handleListIssues(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "id")
	org := orgIDFrom(ctx)
	limit, offset := parsePaging(r)

	params := generated.ListIssuesParams{
		ProjectID: projectID, OrgID: org, Limit: limit, Offset: offset,
	}
	if status := r.URL.Query().Get("status"); status != "" {
		params.Status = pgText(status)
	} else {
		params.Status = pgtype.Text{} // NULL -> all statuses
	}

	issues, err := s.q.ListIssues(ctx, params)
	if err != nil {
		slogError(w, "list issues", err)
		return
	}
	total, err := s.q.CountIssues(ctx, generated.CountIssuesParams{ProjectID: projectID, OrgID: org})
	if err != nil {
		slogError(w, "count issues", err)
		return
	}

	out := make([]issueResponse, 0, len(issues))
	for _, i := range issues {
		out = append(out, toIssueResponse(i))
	}
	writeJSON(w, http.StatusOK, map[string]any{"issues": out, "total": total})
}

func (s *Server) handleGetIssue(w http.ResponseWriter, r *http.Request) {
	i, err := s.q.GetIssue(r.Context(), generated.GetIssueParams{
		ID: chi.URLParam(r, "id"), OrgID: orgIDFrom(r.Context()),
	})
	if err != nil {
		writeErr(w, http.StatusNotFound, "issue not found")
		return
	}
	writeJSON(w, http.StatusOK, toIssueResponse(i))
}

func (s *Server) handleListIssueEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.q.ListEventsByIssue(r.Context(), generated.ListEventsByIssueParams{
		IssueID: pgText(chi.URLParam(r, "id")), OrgID: orgIDFrom(r.Context()), Limit: 50,
	})
	if err != nil {
		slogError(w, "list issue events", err)
		return
	}
	out := make([]eventResponse, 0, len(events))
	for _, e := range events {
		out = append(out, eventResponse{
			ID: e.ID, Level: e.Level, Message: e.Message,
			ExceptionType: e.ExceptionType, ExceptionValue: e.ExceptionValue,
			Platform: e.Platform, Environment: e.Environment, Release: e.Release,
			Stacktrace: e.Stacktrace, ReceivedAt: e.ReceivedAt.Time,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleUpdateIssueStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Status string `json:"status"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	switch req.Status {
	case "unresolved", "resolved", "ignored":
	default:
		writeErr(w, http.StatusBadRequest, "status must be unresolved, resolved, or ignored")
		return
	}
	i, err := s.q.UpdateIssueStatus(r.Context(), generated.UpdateIssueStatusParams{
		ID: chi.URLParam(r, "id"), OrgID: orgIDFrom(r.Context()), Status: req.Status,
	})
	if err != nil {
		writeErr(w, http.StatusNotFound, "issue not found")
		return
	}
	writeJSON(w, http.StatusOK, toIssueResponse(i))
}

func parsePaging(r *http.Request) (limit, offset int32) {
	limit, offset = 50, 0
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 100 {
		limit = int32(v)
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && v >= 0 {
		offset = int32(v)
	}
	return limit, offset
}
