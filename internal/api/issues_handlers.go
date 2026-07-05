package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bright-interaction/flare/internal/db/generated"
	"github.com/bright-interaction/flare/internal/telemetry"
)

type issueResponse struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	Culprit    string    `json:"culprit"`
	Level      string    `json:"level"`
	Status     string    `json:"status"`
	Platform   string    `json:"platform"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
	EventCount   int64     `json:"event_count"`
	GithubURL    string    `json:"github_url"`
	FirstRelease string    `json:"first_release"`
	AITriage     string    `json:"ai_triage"`
	Sensitive    string    `json:"sensitive"`
}

func toIssueResponse(i telemetry.Issue) issueResponse {
	return issueResponse{
		ID: i.ID, Title: i.Title, Culprit: i.Culprit, Level: i.Level,
		Status: i.Status, Platform: i.Platform,
		FirstSeen: i.FirstSeen, LastSeen: i.LastSeen, EventCount: i.EventCount,
		GithubURL: i.GithubURL, FirstRelease: i.FirstRelease, AITriage: i.AITriage,
		Sensitive: i.Sensitive,
	}
}

// genIssueToResponse maps the write-path row (UpdateIssueStatus returns the
// generated type) into the same response shape.
func genIssueToResponse(i *generated.Issue) issueResponse {
	return issueResponse{
		ID: i.ID, Title: i.Title, Culprit: i.Culprit, Level: i.Level,
		Status: i.Status, Platform: i.Platform,
		FirstSeen: i.FirstSeen.Time, LastSeen: i.LastSeen.Time, EventCount: i.EventCount,
		GithubURL: i.GithubUrl, FirstRelease: i.FirstRelease, AITriage: i.AiTriage,
		Sensitive: i.Sensitive,
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

	var status *string
	if v := r.URL.Query().Get("status"); v != "" {
		status = &v
	}

	issues, err := s.store.ListIssues(ctx, projectID, org, limit, offset, status)
	if err != nil {
		slogError(w, "list issues", err)
		return
	}
	total, err := s.store.CountIssues(ctx, projectID, org)
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
	i, err := s.store.GetIssue(r.Context(), chi.URLParam(r, "id"), orgIDFrom(r.Context()))
	if err != nil {
		var nf telemetry.ErrNotFound
		if errors.As(err, &nf) {
			writeErr(w, http.StatusNotFound, "issue not found")
			return
		}
		slogError(w, "get issue", err)
		return
	}
	writeJSON(w, http.StatusOK, toIssueResponse(i))
}

func (s *Server) handleListIssueEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	org := orgIDFrom(ctx)
	issueID := chi.URLParam(r, "id")
	events, err := s.store.ListEventsByIssue(ctx, issueID, org, 50)
	if err != nil {
		slogError(w, "list issue events", err)
		return
	}

	// Symbolicate minified frames using any source maps uploaded for the
	// event's release. Best-effort: resolve the issue's project once, load each
	// distinct release's maps once, and leave frames untouched on any miss.
	releaseMaps := s.releaseSourceMaps(ctx, issueID, org, events)

	out := make([]eventResponse, 0, len(events))
	for _, e := range events {
		stack := e.Stacktrace
		if maps := releaseMaps[e.Release]; len(maps) > 0 {
			stack = s.symbolicator.Symbolicate(stack, maps)
		}
		out = append(out, eventResponse{
			ID: e.ID, Level: e.Level, Message: e.Message,
			ExceptionType: e.ExceptionType, ExceptionValue: e.ExceptionValue,
			Platform: e.Platform, Environment: e.Environment, Release: e.Release,
			Stacktrace: stack, ReceivedAt: e.ReceivedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// releaseSourceMaps loads the source maps (name -> content) for each distinct
// release across the issue's events, keyed by release. Returns an empty map on
// any error so symbolication simply no-ops.
func (s *Server) releaseSourceMaps(ctx context.Context, issueID, org string, events []telemetry.Event) map[string]map[string]string {
	releases := map[string]bool{}
	for _, e := range events {
		if e.Release != "" {
			releases[e.Release] = true
		}
	}
	if len(releases) == 0 {
		return nil
	}
	issue, err := s.store.GetIssue(ctx, issueID, org)
	if err != nil {
		return nil
	}
	out := make(map[string]map[string]string, len(releases))
	for rel := range releases {
		rows, err := s.q.GetSourceMapsForRelease(ctx, generated.GetSourceMapsForReleaseParams{
			ProjectID: issue.ProjectID, OrgID: org, Release: rel,
		})
		if err != nil || len(rows) == 0 {
			continue
		}
		m := make(map[string]string, len(rows))
		for _, row := range rows {
			m[row.Name] = row.Content
		}
		out[rel] = m
	}
	return out
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
	writeJSON(w, http.StatusOK, genIssueToResponse(i))
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
