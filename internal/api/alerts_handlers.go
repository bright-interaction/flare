package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/bright-interaction/flare/internal/db/generated"
	"github.com/bright-interaction/flare/internal/id"
)

type channelResponse struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Config  json.RawMessage `json:"config"`
	Enabled bool            `json:"enabled"`
}

func (s *Server) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type   string          `json:"type"`
		Config json.RawMessage `json:"config"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	switch req.Type {
	case "log":
		if len(req.Config) == 0 {
			req.Config = json.RawMessage(`{}`)
		}
	case "webhook":
		var cfg struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(req.Config, &cfg) != nil || !strings.HasPrefix(cfg.URL, "http") {
			writeErr(w, http.StatusBadRequest, "webhook channel requires config.url (http/https)")
			return
		}
	case "slack":
		var cfg struct {
			WebhookURL string `json:"webhook_url"`
		}
		if json.Unmarshal(req.Config, &cfg) != nil || !strings.HasPrefix(cfg.WebhookURL, "https://") {
			writeErr(w, http.StatusBadRequest, "slack channel requires config.webhook_url (https incoming webhook)")
			return
		}
	case "email":
		var cfg struct {
			To string `json:"to"`
		}
		if json.Unmarshal(req.Config, &cfg) != nil || !strings.Contains(cfg.To, "@") {
			writeErr(w, http.StatusBadRequest, "email channel requires a valid config.to address")
			return
		}
		if !s.mailer.Enabled() {
			writeErr(w, http.StatusBadRequest, "email delivery is not configured on this server (set SMTP_*)")
			return
		}
	default:
		writeErr(w, http.StatusBadRequest, "type must be email, slack, webhook, or log")
		return
	}

	ch, err := s.q.CreateNotificationChannel(r.Context(), generated.CreateNotificationChannelParams{
		ID: id.New(), OrgID: orgIDFrom(r.Context()), Type: req.Type, Config: req.Config, Enabled: true,
	})
	if err != nil {
		slogError(w, "create channel", err)
		return
	}
	writeJSON(w, http.StatusCreated, channelResponse{ID: ch.ID, Type: ch.Type, Config: ch.Config, Enabled: ch.Enabled})
}

func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	chans, err := s.q.ListNotificationChannelsByOrg(r.Context(), orgIDFrom(r.Context()))
	if err != nil {
		slogError(w, "list channels", err)
		return
	}
	out := make([]channelResponse, 0, len(chans))
	for _, c := range chans {
		out = append(out, channelResponse{ID: c.ID, Type: c.Type, Config: c.Config, Enabled: c.Enabled})
	}
	writeJSON(w, http.StatusOK, out)
}

type alertRuleResponse struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Type          string `json:"type"`
	Threshold     int32  `json:"threshold"`
	WindowMinutes int32  `json:"window_minutes"`
	Enabled       bool   `json:"enabled"`
}

func (s *Server) handleCreateAlertRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          string `json:"name"`
		Type          string `json:"type"`
		Threshold     int32  `json:"threshold"`
		WindowMinutes int32  `json:"window_minutes"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Type == "" {
		req.Type = "new_issue"
	}
	switch req.Type {
	case "new_issue", "regression":
		req.Threshold, req.WindowMinutes = 0, 0
	case "spike":
		if req.Threshold < 1 || req.WindowMinutes < 1 {
			writeErr(w, http.StatusBadRequest, "spike rules need a threshold (events) and window (minutes) of at least 1")
			return
		}
	default:
		writeErr(w, http.StatusBadRequest, "type must be new_issue, regression or spike")
		return
	}

	rule, err := s.q.CreateAlertRule(r.Context(), generated.CreateAlertRuleParams{
		ID:            id.New(),
		ProjectID:     chi.URLParam(r, "id"),
		OrgID:         orgIDFrom(r.Context()),
		Name:          req.Name,
		Type:          req.Type,
		Threshold:     req.Threshold,
		WindowMinutes: req.WindowMinutes,
		Enabled:       true,
	})
	if err != nil {
		slogError(w, "create alert rule", err)
		return
	}
	writeJSON(w, http.StatusCreated, alertRuleResponse{
		ID: rule.ID, Name: rule.Name, Type: rule.Type, Threshold: rule.Threshold, WindowMinutes: rule.WindowMinutes, Enabled: rule.Enabled,
	})
}

func (s *Server) handleDeleteAlertRule(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.DeleteAlertRule(r.Context(), generated.DeleteAlertRuleParams{
		ID:        chi.URLParam(r, "ruleID"),
		ProjectID: chi.URLParam(r, "id"),
		OrgID:     orgIDFrom(r.Context()),
	})
	if err != nil {
		slogError(w, "delete alert rule", err)
		return
	}
	if rows == 0 {
		writeErr(w, http.StatusNotFound, "alert rule not found")
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.DeleteNotificationChannel(r.Context(), generated.DeleteNotificationChannelParams{
		ID:    chi.URLParam(r, "id"),
		OrgID: orgIDFrom(r.Context()),
	})
	if err != nil {
		slogError(w, "delete channel", err)
		return
	}
	if rows == 0 {
		writeErr(w, http.StatusNotFound, "channel not found")
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

func (s *Server) handleListAlertRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.q.ListAlertRulesByProject(r.Context(), generated.ListAlertRulesByProjectParams{
		ProjectID: chi.URLParam(r, "id"), OrgID: orgIDFrom(r.Context()),
	})
	if err != nil {
		slogError(w, "list alert rules", err)
		return
	}
	out := make([]alertRuleResponse, 0, len(rules))
	for _, rule := range rules {
		out = append(out, alertRuleResponse{
			ID: rule.ID, Name: rule.Name, Type: rule.Type, Threshold: rule.Threshold, WindowMinutes: rule.WindowMinutes, Enabled: rule.Enabled,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
