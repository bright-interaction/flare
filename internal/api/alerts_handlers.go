package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/bright-interaction/flare/internal/alerts"
	"github.com/bright-interaction/flare/internal/db/generated"
	"github.com/bright-interaction/flare/internal/id"
)

type channelResponse struct {
	ID            string          `json:"id"`
	Type          string          `json:"type"`
	Config        json.RawMessage `json:"config"`
	Enabled       bool            `json:"enabled"`
	LastAttemptAt *string         `json:"last_attempt_at"`
	LastOkAt      *string         `json:"last_ok_at"`
	LastError     string          `json:"last_error"`
	// ProjectIDs is the routing list. EMPTY MEANS ALL PROJECTS, which is both
	// the pre-routing behaviour and the default, so an existing channel keeps
	// receiving everything until someone narrows it deliberately.
	ProjectIDs []string `json:"project_ids"`
}

// toChannelResponse maps a stored channel row to the API shape, redacting the
// secret and surfacing the delivery-health columns so the UI can show whether
// the channel last succeeded or failed (and why).
func (s *Server) toChannelResponse(c *generated.NotificationChannel) channelResponse {
	return channelResponse{
		ID:            c.ID,
		Type:          c.Type,
		Config:        s.redactChannelConfig(c.Type, c.Config),
		Enabled:       c.Enabled,
		LastAttemptAt: tsPtr(c.LastAttemptAt),
		LastOkAt:      tsPtr(c.LastOkAt),
		LastError:     textStr(c.LastError),
	}
}

// channelSecretField returns the config key holding a bearer secret (a Slack
// incoming-webhook URL or a generic webhook URL - whoever holds it can post into
// the org's channel), or "" for channel types with no secret.
func channelSecretField(chType string) string {
	switch chType {
	case "webhook":
		return "url"
	case "slack":
		return "webhook_url"
	}
	return ""
}

// mapChannelSecret applies fn to the secret-bearing field of a channel config
// and returns the rewritten JSON. On any parse error it returns the input
// unchanged so a malformed row never breaks dispatch.
func mapChannelSecret(chType string, cfg json.RawMessage, fn func(string) string) json.RawMessage {
	field := channelSecretField(chType)
	if field == "" {
		return cfg
	}
	var m map[string]any
	if json.Unmarshal(cfg, &m) != nil {
		return cfg
	}
	if v, ok := m[field].(string); ok && v != "" {
		m[field] = fn(v)
	}
	b, err := json.Marshal(m)
	if err != nil {
		return cfg
	}
	return b
}

// encryptChannelConfig / decryptChannelConfig encrypt the webhook secret at rest
// so a DB dump does not expose it; decryption happens only at the dispatch
// boundary just before the alert is sent.
func (s *Server) encryptChannelConfig(chType string, cfg json.RawMessage) json.RawMessage {
	return mapChannelSecret(chType, cfg, s.secrets.Encrypt)
}
func (s *Server) decryptChannelConfig(chType string, cfg json.RawMessage) json.RawMessage {
	return mapChannelSecret(chType, cfg, s.secrets.Decrypt)
}

// redactChannelConfig decrypts then masks the webhook secret before it is
// returned by any read handler: never re-displayed in full, only host plus a
// short suffix for identification, mirroring the "shown once" rule for API keys
// and the GitHub token. Email destinations and the empty log config are kept.
func (s *Server) redactChannelConfig(chType string, cfg json.RawMessage) json.RawMessage {
	return mapChannelSecret(chType, cfg, func(v string) string { return maskURL(s.secrets.Decrypt(v)) })
}

// maskURL keeps scheme://host and a 4-char suffix, dropping the secret path so
// the value identifies the endpoint without being replayable.
func maskURL(raw string) string {
	scheme, host := "", raw
	if i := strings.Index(raw, "://"); i >= 0 {
		scheme = raw[:i+3]
		host = raw[i+3:]
	}
	if j := strings.IndexByte(host, '/'); j >= 0 {
		host = host[:j]
	}
	tail := ""
	if len(raw) >= 4 {
		tail = raw[len(raw)-4:]
	}
	return scheme + host + "/..." + tail
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
		ID: id.New(), OrgID: orgIDFrom(r.Context()), Type: req.Type, Config: s.encryptChannelConfig(req.Type, req.Config), Enabled: true,
	})
	if err != nil {
		slogError(w, "create channel", err)
		return
	}
	writeJSON(w, http.StatusCreated, s.toChannelResponse(ch))
}

func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	chans, err := s.q.ListNotificationChannelsByOrg(r.Context(), orgIDFrom(r.Context()))
	if err != nil {
		slogError(w, "list channels", err)
		return
	}
	out := make([]channelResponse, 0, len(chans))
	for _, c := range chans {
		resp := s.toChannelResponse(c)
		// Routing, so the UI can show and edit it. A read error here must NOT
		// degrade to empty: empty renders as "all projects", so a transient
		// failure would display a narrowed channel as an estate-wide broadcast,
		// and a subsequent save would then persist that wider set. Fail closed
		// (500) rather than show a channel as broader than it really is.
		pids, err := s.q.ListChannelProjects(r.Context(), c.ID)
		if err != nil {
			slogError(w, "list channel routing", err)
			return
		}
		resp.ProjectIDs = pids
		out = append(out, resp)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTestChannel sends a synthetic notification to one channel and returns
// the real delivery outcome, so an operator can prove a channel actually
// reaches a human before relying on it. The attempt is recorded like any real
// delivery, so a test also refreshes the channel's health status.
func (s *Server) handleTestChannel(w http.ResponseWriter, r *http.Request) {
	if !s.testLimiter.Allow("test:" + orgIDFrom(r.Context())) {
		writeErr(w, http.StatusTooManyRequests, "too many test sends, wait a moment")
		return
	}
	ch, err := s.q.GetNotificationChannel(r.Context(), generated.GetNotificationChannelParams{
		ID: chi.URLParam(r, "id"), OrgID: orgIDFrom(r.Context()),
	})
	if err != nil {
		writeErr(w, http.StatusNotFound, "channel not found")
		return
	}
	n := alerts.Notification{
		ProjectName: "Flare",
		Title:       "Test notification from Flare",
		Level:       "info",
		Reason:      "Test",
		EventCount:  1,
		URL:         strings.TrimRight(s.cfg.BaseURL, "/") + "/settings",
	}
	if derr := s.dispatcher.DispatchOne(r.Context(), alerts.Channel{
		ID: ch.ID, OrgID: ch.OrgID, Type: ch.Type, Config: s.decryptChannelConfig(ch.Type, ch.Config),
	}, n); derr != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": derr.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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
	case "anomaly":
		// threshold = baseline multiplier (>=2), window_minutes = the recent
		// window compared against the trailing 24h baseline. Cap the window at
		// 720m (12h): beyond that there is <2 windows in a day, so there is no
		// baseline to compare against and the rule could never fire.
		if req.WindowMinutes < 1 || req.WindowMinutes > 720 {
			writeErr(w, http.StatusBadRequest, "anomaly rules need a window (minutes) between 1 and 720")
			return
		}
		if req.Threshold < 2 {
			req.Threshold = 3 // default: fire at 3x baseline
		}
	case "silence":
		// threshold = minimum events over 24h to count as "normally active",
		// window_minutes = how long silent before firing.
		if req.WindowMinutes < 1 {
			writeErr(w, http.StatusBadRequest, "silence rules need a window (minutes) of at least 1")
			return
		}
		if req.Threshold < 1 {
			req.Threshold = 20
		}
	default:
		writeErr(w, http.StatusBadRequest, "type must be new_issue, regression, spike, anomaly or silence")
		return
	}

	// Verify the path project belongs to the caller's org before writing a rule
	// against it. projects.id is a global primary key, so without this a member
	// could persist an alert rule referencing another org's project. Mirrors the
	// ownership check every project read path performs.
	proj, err := s.q.GetProjectByID(r.Context(), generated.GetProjectByIDParams{
		ID:       chi.URLParam(r, "id"),
		OrgScope: orgIDFrom(r.Context()),
	})
	if err != nil {
		writeErr(w, http.StatusNotFound, "project not found")
		return
	}

	rule, err := s.q.CreateAlertRule(r.Context(), generated.CreateAlertRuleParams{
		ID:            id.New(),
		ProjectID:     proj.ID,
		OrgID:         proj.OrgID,
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

// maxChannelProjects bounds a channel's routing list. A channel realistically
// serves a handful of projects, so 256 is far above any honest fan-out while
// still capping the sequential round trips one PATCH can drive: the 1MB body
// cap in decodeJSON otherwise admits ~40k ids, and the AddChannelProject loop
// issues one INSERT each. De-duplication (below) collapses the "same id 40k
// times" case to one row before this bound is even checked.
const maxChannelProjects = 256

// handleUpdateChannel toggles a channel's enabled flag and its project routing.
//
// notification_channels.enabled has existed since the first schema and the
// dispatch query has always filtered on it, but nothing could ever SET it: the
// routes were create, test and delete. So silencing a channel that started
// paging at 3am meant DELETING it and re-creating it afterwards, re-entering a
// config the API deliberately redacts and will not read back. The column was
// there; only the wiring was missing.
func (s *Server) handleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled    *bool     `json:"enabled"`
		ProjectIDs *[]string `json:"project_ids"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ctx := r.Context()
	org := orgIDFrom(ctx)
	chID := chi.URLParam(r, "id")

	// De-duplicate and cap the routing list BEFORE any DB work, so a
	// duplicate-heavy or oversized body is collapsed or rejected before it can
	// drive thousands of INSERTs. Insertion order is preserved for a stable,
	// testable result.
	var projectIDs []string
	if req.ProjectIDs != nil {
		seen := make(map[string]struct{}, len(*req.ProjectIDs))
		projectIDs = make([]string, 0, len(*req.ProjectIDs))
		for _, pid := range *req.ProjectIDs {
			if _, dup := seen[pid]; dup {
				continue
			}
			seen[pid] = struct{}{}
			projectIDs = append(projectIDs, pid)
		}
		if len(projectIDs) > maxChannelProjects {
			writeErr(w, http.StatusBadRequest, "too many project_ids (max 256)")
			return
		}
	}

	// Everything runs in ONE transaction: the ownership checks, the enabled
	// write, the per-project org validation, and the routing replace+insert.
	// All validation happens before the first mutation, so nothing is written
	// unless everything is valid. MVCC hides the DELETE-then-INSERT window from
	// every other reader (so a concurrent dispatch never sees the empty
	// channel_projects state, which reads as "all projects"), and any error
	// rolls the whole PATCH back: a failed update leaves both the original
	// routing and the original enabled flag untouched.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		slogError(w, "update channel: begin tx", err)
		return
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)

	if req.Enabled != nil {
		rows, err := qtx.SetChannelEnabled(ctx, generated.SetChannelEnabledParams{
			ID: chID, OrgID: org, Enabled: *req.Enabled,
		})
		if err != nil {
			slogError(w, "set channel enabled", err)
			return
		}
		if rows == 0 {
			writeErr(w, http.StatusNotFound, "channel not found")
			return
		}
	}

	if req.ProjectIDs != nil {
		// Verify every project belongs to THIS org before storing it. The join
		// table has no org_id of its own, so this handler is the only thing
		// standing between a routing list and a cross-tenant reference.
		for _, pid := range projectIDs {
			if _, err := qtx.GetProjectByID(ctx, generated.GetProjectByIDParams{ID: pid, OrgScope: org}); err != nil {
				writeErr(w, http.StatusBadRequest, "unknown project in project_ids")
				return
			}
		}
		// Confirm the channel is this org's before touching its routing: without
		// this, an id from another tenant would have its routing rewritten.
		if _, err := qtx.GetNotificationChannel(ctx, generated.GetNotificationChannelParams{ID: chID, OrgID: org}); err != nil {
			writeErr(w, http.StatusNotFound, "channel not found")
			return
		}
		if err := qtx.ReplaceChannelProjects(ctx, chID); err != nil {
			slogError(w, "clear channel routing", err)
			return
		}
		for _, pid := range projectIDs {
			if err := qtx.AddChannelProject(ctx, generated.AddChannelProjectParams{
				ChannelID: chID, ProjectID: pid,
			}); err != nil {
				slogError(w, "set channel routing", err)
				return
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		slogError(w, "update channel: commit", err)
		return
	}

	s.audit(ctx, "channel.update", chID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
