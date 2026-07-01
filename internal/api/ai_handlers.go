package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/bright-interaction/flare/internal/ai"
	"github.com/bright-interaction/flare/internal/db/generated"
	"github.com/bright-interaction/flare/internal/telemetry"
)

type aiConfigResponse struct {
	Enabled bool   `json:"enabled"`
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	Format  string `json:"format"`
}

func (s *Server) handleGetAIConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.q.GetAIConfig(r.Context(), orgIDFrom(r.Context()))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, aiConfigResponse{Format: "openai"})
		return
	}
	if err != nil {
		slogError(w, "get ai config", err)
		return
	}
	writeJSON(w, http.StatusOK, aiConfigResponse{Enabled: cfg.Enabled, BaseURL: cfg.BaseUrl, Model: cfg.Model, Format: cfg.Format})
}

func (s *Server) handleSetAIConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
		Model   string `json:"model"`
		Format  string `json:"format"`
		Enabled bool   `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.BaseURL = strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	req.Model = strings.TrimSpace(req.Model)
	req.APIKey = strings.TrimSpace(req.APIKey)
	if req.Format == "" {
		req.Format = "openai"
	}
	if !strings.HasPrefix(req.BaseURL, "https://") || req.Model == "" {
		writeErr(w, http.StatusBadRequest, "base_url (https) and model are required")
		return
	}
	if req.Format != "openai" && req.Format != "anthropic" {
		writeErr(w, http.StatusBadRequest, "format must be openai or anthropic")
		return
	}
	// Blank key on update keeps the stored one.
	if req.APIKey == "" {
		existing, err := s.q.GetAIConfig(r.Context(), orgIDFrom(r.Context()))
		if err != nil || existing.ApiKey == "" {
			writeErr(w, http.StatusBadRequest, "an api key is required")
			return
		}
		req.APIKey = existing.ApiKey
	}
	if err := s.q.UpsertAIConfig(r.Context(), generated.UpsertAIConfigParams{
		OrgID: orgIDFrom(r.Context()), BaseUrl: req.BaseURL, ApiKey: req.APIKey,
		Model: req.Model, Format: req.Format, Enabled: req.Enabled,
	}); err != nil {
		slogError(w, "save ai config", err)
		return
	}
	s.audit(r.Context(), "ai.configure", req.BaseURL)
	s.handleGetAIConfig(w, r)
}

func (s *Server) handleDeleteAIConfig(w http.ResponseWriter, r *http.Request) {
	if err := s.q.DeleteAIConfig(r.Context(), orgIDFrom(r.Context())); err != nil {
		slogError(w, "delete ai config", err)
		return
	}
	s.audit(r.Context(), "ai.disconnect", "")
	writeJSON(w, http.StatusNoContent, nil)
}

// handleTriageIssue runs (or returns cached) AI triage for an issue: a plain
// language explanation, likely root cause, and suggested fix, built from the
// symbolicated stack trace. PII is scrubbed before it reaches the model.
func (s *Server) handleTriageIssue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	org := orgIDFrom(ctx)
	issueID := chi.URLParam(r, "id")

	issue, err := s.store.GetIssue(ctx, issueID, org)
	if err != nil {
		var nf telemetry.ErrNotFound
		if errors.As(err, &nf) {
			writeErr(w, http.StatusNotFound, "issue not found")
			return
		}
		slogError(w, "triage: load issue", err)
		return
	}
	if issue.AITriage != "" && r.URL.Query().Get("refresh") != "true" {
		writeJSON(w, http.StatusOK, map[string]any{"triage": issue.AITriage, "cached": true})
		return
	}

	cfg, err := s.q.GetAIConfig(ctx, org)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !cfg.Enabled) {
		writeErr(w, http.StatusBadRequest, "AI triage is not configured for this workspace")
		return
	}
	if err != nil {
		slogError(w, "triage: config", err)
		return
	}

	prompt := s.buildTriageContext(ctx, issue, org)
	system := "You are a senior software engineer triaging a production error from an observability tool. " +
		"The stack trace has been de-minified to original source. Reply in concise GitHub-flavored markdown with three short sections: " +
		"**What happened** (plain language), **Likely root cause**, and **Suggested fix** (concrete, code-level where possible). " +
		"Only use what the report supports; do not invent details."

	triage, err := s.ai.Complete(ctx, ai.Config{BaseURL: cfg.BaseUrl, APIKey: cfg.ApiKey, Model: cfg.Model, Format: cfg.Format}, system, prompt)
	if err != nil {
		slogError(w, "ai triage", err)
		writeErr(w, http.StatusBadGateway, "the AI endpoint did not respond (check the base URL, key and model)")
		return
	}
	if err := s.q.SetIssueTriage(ctx, generated.SetIssueTriageParams{ID: issue.ID, OrgID: org, AiTriage: triage}); err != nil {
		slogError(w, "triage: save", err)
		return
	}
	s.audit(ctx, "ai.triage", issue.Title)
	writeJSON(w, http.StatusOK, map[string]any{"triage": triage, "cached": false})
}

type triageFrame struct {
	Filename    string `json:"filename"`
	Function    string `json:"function"`
	Lineno      int    `json:"lineno"`
	ContextLine string `json:"context_line"`
}

// buildTriageContext assembles a scrubbed, model-ready description of the issue
// from its latest event's symbolicated stack trace. PII is scrubbed last so
// nothing personal leaves the tenant boundary.
func (s *Server) buildTriageContext(ctx context.Context, issue telemetry.Issue, org string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Error: %s\n", issue.Title)
	fmt.Fprintf(&b, "Level: %s   Release: %s\n", issue.Level, issue.FirstRelease)
	if issue.Culprit != "" {
		fmt.Fprintf(&b, "Location: %s\n", issue.Culprit)
	}

	events, _ := s.store.ListEventsByIssue(ctx, issue.ID, org, 1)
	if len(events) > 0 {
		e := events[0]
		if e.ExceptionType != "" || e.ExceptionValue != "" {
			fmt.Fprintf(&b, "Exception: %s: %s\n", e.ExceptionType, e.ExceptionValue)
		}
		stack := e.Stacktrace
		if maps := s.releaseSourceMaps(ctx, issue.ID, org, events)[e.Release]; len(maps) > 0 {
			stack = s.symbolicator.Symbolicate(stack, maps)
		}
		var st struct {
			Frames []triageFrame `json:"frames"`
		}
		if json.Unmarshal(stack, &st) == nil && len(st.Frames) > 0 {
			b.WriteString("\nStack trace (most recent call last):\n")
			frames := st.Frames
			if len(frames) > 30 {
				frames = frames[len(frames)-30:]
			}
			for _, f := range frames {
				fn := f.Function
				if fn == "" {
					fn = "?"
				}
				fmt.Fprintf(&b, "  %s at %s:%d\n", fn, f.Filename, f.Lineno)
				if f.ContextLine != "" {
					fmt.Fprintf(&b, "      > %s\n", strings.TrimSpace(f.ContextLine))
				}
			}
		}
	}
	return ai.Scrub(b.String())
}
