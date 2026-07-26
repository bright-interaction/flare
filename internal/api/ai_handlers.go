package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/bright-interaction/flare/internal/ai"
	"github.com/bright-interaction/flare/internal/db/generated"
	"github.com/bright-interaction/flare/internal/telemetry"
)

type aiConfigResponse struct {
	Enabled           bool   `json:"enabled"`
	BaseURL           string `json:"base_url"`
	Model             string `json:"model"`
	Format            string `json:"format"`
	AutoTriage        bool   `json:"auto_triage"`
	TriageDailyBudget int32  `json:"triage_daily_budget"`
}

// defaultTriageDailyBudget bounds auto-triage BYOAI spend for an org that never
// set an explicit budget.
const defaultTriageDailyBudget = 50

func (s *Server) handleGetAIConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.q.GetAIConfig(r.Context(), orgIDFrom(r.Context()))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, aiConfigResponse{Format: "openai", TriageDailyBudget: defaultTriageDailyBudget})
		return
	}
	if err != nil {
		slogError(w, "get ai config", err)
		return
	}
	writeJSON(w, http.StatusOK, aiConfigResponse{
		Enabled: cfg.Enabled, BaseURL: cfg.BaseUrl, Model: cfg.Model, Format: cfg.Format,
		AutoTriage: cfg.AutoTriage, TriageDailyBudget: cfg.TriageDailyBudget,
	})
}

func (s *Server) handleSetAIConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BaseURL           string `json:"base_url"`
		APIKey            string `json:"api_key"`
		Model             string `json:"model"`
		Format            string `json:"format"`
		Enabled           bool   `json:"enabled"`
		AutoTriage        bool   `json:"auto_triage"`
		TriageDailyBudget int32  `json:"triage_daily_budget"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.TriageDailyBudget < 0 || req.TriageDailyBudget > 10000 {
		writeErr(w, http.StatusBadRequest, "triage_daily_budget must be between 0 and 10000")
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
	// The api_key column has two possible origins and they are NOT the same trust
	// level, so they take separate paths. A supplied key is client input and is
	// always encrypted. A blank key means "keep the stored one", and that value
	// is already encrypted at rest, so it is carried through untouched.
	//
	// Do not collapse these into a single Encrypt call that decides by looking at
	// the string. Letting a prefix on client input skip encryption is a
	// decryption oracle, and here it is an exfiltration primitive: base_url is
	// client-controlled too, so the decrypted victim key would be sent as a
	// Bearer token to a host the attacker picked.
	storedAPIKey := ""
	if req.APIKey == "" {
		existing, err := s.q.GetAIConfig(r.Context(), orgIDFrom(r.Context()))
		if err != nil || existing.ApiKey == "" {
			writeErr(w, http.StatusBadRequest, "an api key is required")
			return
		}
		storedAPIKey = existing.ApiKey
	}
	apiKeyColumn := secretColumnForUpdate(s.secrets, req.APIKey, storedAPIKey)
	if err := s.q.UpsertAIConfig(r.Context(), generated.UpsertAIConfigParams{
		OrgID: orgIDFrom(r.Context()), BaseUrl: req.BaseURL, ApiKey: apiKeyColumn,
		Model: req.Model, Format: req.Format, Enabled: req.Enabled,
		AutoTriage: req.AutoTriage, TriageDailyBudget: req.TriageDailyBudget,
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
// Sentinel errors so callers (the REST handler and the MCP tool) can map a
// triage failure to the right status/message without string-matching. The
// "not configured" text is load-bearing: the frontend and MCP surface a
// Settings hint when they see it.
var (
	errTriageNotConfigured = errors.New("AI triage is not configured for this workspace")
	errTriageEndpoint      = errors.New("the AI endpoint did not respond (check the base URL, key and model)")
)

func (s *Server) handleTriageIssue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	org := orgIDFrom(ctx)
	issueID := chi.URLParam(r, "id")

	text, cached, err := s.triageIssue(ctx, org, issueID, r.URL.Query().Get("refresh") == "true")
	if err != nil {
		var nf telemetry.ErrNotFound
		switch {
		case errors.As(err, &nf):
			writeErr(w, http.StatusNotFound, "issue not found")
		case errors.Is(err, errTriageNotConfigured):
			writeErr(w, http.StatusBadRequest, errTriageNotConfigured.Error())
		case errors.Is(err, errTriageEndpoint):
			writeErr(w, http.StatusBadGateway, errTriageEndpoint.Error())
		default:
			slogError(w, "ai triage", err)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"triage": text, "cached": cached})
}

// triageIssue runs (or returns cached) AI triage for one issue. Shared by the
// REST handler and the MCP tool. Returns the triage text, whether it was
// served from cache, and a typed error (errTriageNotConfigured /
// errTriageEndpoint / telemetry.ErrNotFound / raw DB error).
func (s *Server) triageIssue(ctx context.Context, org, issueID string, refresh bool) (string, bool, error) {
	issue, err := s.store.GetIssue(ctx, issueID, org)
	if err != nil {
		return "", false, err
	}
	if issue.AITriage != "" && !refresh {
		return issue.AITriage, true, nil
	}

	cfg, err := s.q.GetAIConfig(ctx, org)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !cfg.Enabled) {
		return "", false, errTriageNotConfigured
	}
	if err != nil {
		return "", false, err
	}

	// The report is written by whoever sent the event, not by us: a DSN public
	// key lives in a browser bundle, so the exception message and every stack
	// frame are anonymous input. Fence it and tell the model what the fence
	// means, because the answer does not stop at the dashboard: it is served
	// back through the MCP tools to the operator's own agent, which can write.
	report := s.buildTriageContext(ctx, issue, org)
	fenced, untrustedRule, err := ai.Fence(report)
	if err != nil {
		return "", false, fmt.Errorf("%w: %v", errTriageEndpoint, err)
	}
	system := "You are a senior software engineer triaging a production error from an observability tool. " +
		"The stack trace has been de-minified to original source. Reply in concise GitHub-flavored markdown with three short sections: " +
		"**What happened** (plain language), **Likely root cause**, and **Suggested fix** (concrete, code-level where possible). " +
		"Only use what the report supports; do not invent details. " +
		untrustedRule

	triage, err := s.ai.Complete(ctx, ai.Config{BaseURL: cfg.BaseUrl, APIKey: s.secrets.Decrypt(cfg.ApiKey), Model: cfg.Model, Format: cfg.Format}, system, fenced)
	if err != nil {
		return "", false, fmt.Errorf("%w: %v", errTriageEndpoint, err)
	}
	// The reply is untrusted too, and for a second reason on top of the
	// injected-telemetry one: base_url is org-configurable, so the endpoint
	// answering is not necessarily a model at all. Strip control characters and
	// cap the size before it is persisted and re-served.
	triage = ai.Block(triage)
	if err := s.q.SetIssueTriage(ctx, generated.SetIssueTriageParams{ID: issue.ID, OrgID: org, AiTriage: triage}); err != nil {
		return "", false, err
	}
	s.audit(ctx, "ai.triage", issue.Title)
	return triage, false, nil
}

// maybeAutoTriage runs AI triage on a newly-seen issue in the background when
// the org has auto-triage enabled and is under its daily budget. Fire-and-forget
// with a detached context: it must never block or fail ingest. The budget is
// claimed atomically BEFORE the model call, so a burst of new fingerprints
// cannot run more than triage_daily_budget BYOAI completions in a day.
func (s *Server) maybeAutoTriage(org, issueID string) {
	s.goBackground("auto-triage", 90*time.Second, func(ctx context.Context) {
		cfg, err := s.q.GetAIConfig(ctx, org)
		if err != nil || !cfg.Enabled || !cfg.AutoTriage || cfg.TriageDailyBudget <= 0 {
			return
		}
		if _, err := s.q.ClaimAITriageBudget(ctx, generated.ClaimAITriageBudgetParams{
			OrgID: org, Budget: cfg.TriageDailyBudget,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				slog.Info("auto-triage skipped: daily budget reached", "org", org, "budget", cfg.TriageDailyBudget)
			} else {
				slog.Warn("auto-triage budget claim failed", "org", org, "error", err)
			}
			return
		}
		if _, _, err := s.triageIssue(ctx, org, issueID, false); err != nil {
			slog.Warn("auto-triage failed", "issue", issueID, "error", err)
		}
	})
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
	// Every value below arrives on the ingest endpoint, so each one is flattened
	// to a single line first. Otherwise a title of "boom\n\nLocation: ..." lets
	// the sender forge additional report sections, which is the cheapest version
	// of the injection: text that reads as though Flare wrote it.
	fmt.Fprintf(&b, "Error: %s\n", ai.Line(issue.Title))
	fmt.Fprintf(&b, "Level: %s   Release: %s\n", ai.Line(issue.Level), ai.Line(issue.FirstRelease))
	if issue.Culprit != "" {
		fmt.Fprintf(&b, "Location: %s\n", ai.Line(issue.Culprit))
	}

	events, _ := s.store.ListEventsByIssue(ctx, issue.ID, org, 1)
	if len(events) > 0 {
		e := events[0]
		if e.ExceptionType != "" || e.ExceptionValue != "" {
			fmt.Fprintf(&b, "Exception: %s: %s\n", ai.Line(e.ExceptionType), ai.Line(e.ExceptionValue))
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
				fn := ai.Line(f.Function)
				if fn == "" {
					fn = "?"
				}
				fmt.Fprintf(&b, "  %s at %s:%d\n", fn, ai.Line(f.Filename), f.Lineno)
				if cl := ai.Line(f.ContextLine); cl != "" {
					fmt.Fprintf(&b, "      > %s\n", cl)
				}
			}
		}
	}
	// Scrub last so PII never leaves the tenant boundary, then cap the whole
	// region: 30 frames of attacker-sized context lines would otherwise be an
	// unbounded prompt on the tenant's own BYOAI key.
	return ai.Block(ai.Scrub(b.String()))
}
