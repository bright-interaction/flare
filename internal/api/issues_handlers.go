package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/bright-interaction/flare/internal/ai"
	"github.com/bright-interaction/flare/internal/db/generated"
	"github.com/bright-interaction/flare/internal/ingest"
	"github.com/bright-interaction/flare/internal/telemetry"
)

type issueResponse struct {
	ID           string    `json:"id"`
	ProjectID    string    `json:"project_id"`
	Title        string    `json:"title"`
	Culprit      string    `json:"culprit"`
	Level        string    `json:"level"`
	Status       string    `json:"status"`
	Platform     string    `json:"platform"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
	EventCount   int64     `json:"event_count"`
	GithubURL    string    `json:"github_url"`
	FirstRelease string    `json:"first_release"`
	AITriage     string    `json:"ai_triage"`
	Sensitive    string    `json:"sensitive"`
}

func toIssueResponse(i telemetry.Issue) issueResponse {
	title, culprit := i.Title, i.Culprit
	if i.Sensitive != "" {
		// The detector flagged a secret/PII value in this issue's telemetry.
		// Scrub it out of the strings echoed back so the leaked value is not
		// re-served to any reader through the API/UI.
		title, culprit = ai.Scrub(title), ai.Scrub(culprit)
	}
	return issueResponse{
		ID: i.ID, ProjectID: i.ProjectID, Title: title, Culprit: culprit, Level: i.Level,
		Status: i.Status, Platform: i.Platform,
		FirstSeen: i.FirstSeen, LastSeen: i.LastSeen, EventCount: i.EventCount,
		GithubURL: i.GithubURL, FirstRelease: i.FirstRelease, AITriage: i.AITriage,
		Sensitive: i.Sensitive,
	}
}

// genIssueToResponse maps the write-path row (UpdateIssueStatus returns the
// generated type) into the same response shape.
func genIssueToResponse(i *generated.Issue) issueResponse {
	title, culprit := i.Title, i.Culprit
	if i.Sensitive != "" {
		title, culprit = ai.Scrub(title), ai.Scrub(culprit)
	}
	return issueResponse{
		ID: i.ID, ProjectID: i.ProjectID, Title: title, Culprit: culprit, Level: i.Level,
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
	TraceID        string          `json:"trace_id"`
	SpanID         string          `json:"span_id"`
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
	var q *string
	if v := strings.TrimSpace(r.URL.Query().Get("q")); v != "" {
		// Rune-safe: a raw v[:200] byte slice splits a multi-byte character and
		// hands Postgres invalid UTF-8, which errors the whole query. Same bug
		// class as the issue-title truncation fixed in internal/ingest.
		v = escapeLike(ingest.SanitizeText(truncateRunes(v, 200)))
		q = &v
	}

	issues, err := s.store.ListIssues(ctx, projectID, org, limit, offset, status, q)
	if err != nil {
		slogError(w, "list issues", err)
		return
	}
	// Same filter as the list, so the total always describes what is shown.
	total, err := s.store.CountIssues(ctx, projectID, org, status, q)
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
	// If the issue is flagged sensitive, scrub the leaked value out of the
	// event text before returning it. Missing issue -> no scrub (no events).
	issue, _ := s.store.GetIssue(ctx, issueID, org)
	writeJSON(w, http.StatusOK, s.toEventResponses(ctx, issueID, org, events, issue.Sensitive != ""))
}

// toEventResponses maps stored events to the clean API shape, symbolicating
// minified frames using any source maps uploaded for each event's release
// (best-effort: distinct releases are loaded once, frames left untouched on a
// miss). When sensitive is set (the issue's detector flagged a secret/PII
// value), the message and exception value are scrubbed so the leaked value is
// not re-served. Shared by the REST events endpoint and the MCP get_issue tool.
func (s *Server) toEventResponses(ctx context.Context, issueID, org string, events []telemetry.Event, sensitive bool) []eventResponse {
	releaseMaps := s.releaseSourceMaps(ctx, issueID, org, events)
	out := make([]eventResponse, 0, len(events))
	for _, e := range events {
		stack := e.Stacktrace
		if maps := releaseMaps[e.Release]; len(maps) > 0 {
			stack = s.symbolicator.Symbolicate(stack, maps)
		}
		msg, excVal := e.Message, e.ExceptionValue
		if sensitive {
			msg, excVal = ai.Scrub(msg), ai.Scrub(excVal)
		}
		out = append(out, eventResponse{
			ID: e.ID, Level: e.Level, Message: msg,
			ExceptionType: e.ExceptionType, ExceptionValue: excVal,
			Platform: e.Platform, Environment: e.Environment, Release: e.Release,
			Stacktrace: stack, TraceID: e.TraceID, SpanID: e.SpanID, ReceivedAt: e.ReceivedAt,
		})
	}
	return out
}

// scrubIssueForMCP scrubs the issue strings that cross the LLM boundary. It runs
// UNCONDITIONALLY, unlike toIssueResponse, which only scrubs when the detector
// already flagged the issue as sensitive. The detector is a heuristic: a title is
// the raw "ExceptionType: ExceptionValue" from the reporting service, so it can
// carry PII/secrets the detector never matched. REST keeps the flag-conditional
// behaviour, because there the reader is an authenticated operator of that org.
func scrubIssueForMCP(i issueResponse) issueResponse {
	i.Title = ai.Scrub(i.Title)
	i.Culprit = ai.Scrub(i.Culprit)
	i.AITriage = ai.Scrub(i.AITriage)
	i.FirstRelease = ai.Scrub(i.FirstRelease)
	return i
}

// scrubEventsForMCP runs the PII/secret scrubber over event fields that cross the LLM
// boundary via the MCP get_issue tool: the message, the exception value, and the stack
// trace (frame context lines + locals routinely carry request params, DSNs, and secrets
// from the reporting service). Applied ONLY on the MCP path; the REST dashboard shows the
// raw event to the authenticated operator of that org. Mirrors toLogResponsesScrubbed.
func scrubEventsForMCP(events []eventResponse) []eventResponse {
	for i := range events {
		events[i].Message = ai.Scrub(events[i].Message)
		events[i].ExceptionValue = ai.Scrub(events[i].ExceptionValue)
		// Every remaining free-text field on the event is equally
		// client-supplied: an exception type can be a formatted string, and
		// release/environment routinely carry a branch name or a URL.
		events[i].ExceptionType = ai.Scrub(events[i].ExceptionType)
		events[i].Release = ai.Scrub(events[i].Release)
		events[i].Environment = ai.Scrub(events[i].Environment)
		if len(events[i].Stacktrace) > 0 {
			events[i].Stacktrace = json.RawMessage(ai.ScrubJSON(events[i].Stacktrace))
		}
	}
	return events
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
	// Byte budget for the WHOLE request, spent across releases.
	//
	// A row cap does not bound memory here: an artifact may be 30 MiB
	// (maxArtifactBody), so the old LIMIT 1000 allowed 30 GB per release, and an
	// issue whose events span several releases multiplied that again. One
	// GET /api/issues/{id}/events was enough to OOM the shared process, which on
	// a self-host takes down ingest for every tenant with it.
	//
	// 64 MiB comfortably covers a real bundle's maps while making the endpoint's
	// worst case bounded and predictable.
	const maxSourceMapBytes = 64 << 20
	remaining := int64(maxSourceMapBytes)

	out := make(map[string]map[string]string, len(releases))
	for rel := range releases {
		if remaining <= 0 {
			break
		}
		rows, err := s.q.GetSourceMapsForRelease(ctx, generated.GetSourceMapsForReleaseParams{
			ProjectID: issue.ProjectID, OrgID: org, Release: rel,
			MaxBytes: remaining,
		})
		if err != nil || len(rows) == 0 {
			continue
		}
		m := make(map[string]string, len(rows))
		for _, row := range rows {
			m[row.Name] = row.Content
			remaining -= int64(len(row.Content))
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

// escapeLike neutralises LIKE/ILIKE metacharacters so a search term is matched
// literally. The queries declare ESCAPE '\', so the backslash must be escaped
// first or it would swallow the character after it.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// truncateRunes cuts s to at most n bytes without splitting a UTF-8 rune.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
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
