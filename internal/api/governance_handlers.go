package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bright-interaction/flare/internal/db/generated"
	"github.com/bright-interaction/flare/internal/id"
)

// audit appends a sensitive action to the org's audit trail. Best-effort: a
// failure is logged, never propagated to the caller. Actor is the session user
// (NULL for API-key actions).
func (s *Server) audit(ctx context.Context, action, target string) {
	actor := pgtype.Text{}
	if uid := userIDFrom(ctx); uid != "" {
		actor = pgText(uid)
	}
	if err := s.q.InsertAuditLog(ctx, generated.InsertAuditLogParams{
		ID: id.New(), OrgID: orgIDFrom(ctx), ActorUserID: actor, Action: action, Target: target,
	}); err != nil {
		slog.Warn("audit log insert failed", "action", action, "error", err)
	}
}

type auditEntry struct {
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	Actor     string    `json:"actor"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) handleListAuditLog(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.ListAuditLog(r.Context(), orgIDFrom(r.Context()))
	if err != nil {
		slogError(w, "list audit log", err)
		return
	}
	out := make([]auditEntry, 0, len(rows))
	for _, row := range rows {
		actor := "API key"
		if row.ActorEmail.Valid {
			actor = row.ActorEmail.String
		}
		out = append(out, auditEntry{Action: row.Action, Target: row.Target, Actor: actor, CreatedAt: row.CreatedAt.Time})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleExport returns the org's structured data as a downloadable JSON bundle
// (data portability). Raw events/logs/spans are excluded by size; issue
// metadata is included.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	org := orgIDFrom(ctx)

	orgRow, err := s.q.GetOrgByID(ctx, org)
	if err != nil {
		slogError(w, "export: org", err)
		return
	}
	users, _ := s.q.ListUsersByOrg(ctx, org)
	channels, _ := s.q.ListNotificationChannelsByOrg(ctx, org)
	projects, _ := s.q.ListProjectsByOrg(ctx, org)

	members := make([]map[string]any, 0, len(users))
	for _, u := range users {
		members = append(members, map[string]any{"email": u.Email, "role": u.Role, "created_at": u.CreatedAt.Time})
	}
	chans := make([]map[string]any, 0, len(channels))
	for _, c := range channels {
		chans = append(chans, map[string]any{"type": c.Type, "config": c.Config, "enabled": c.Enabled})
	}

	projOut := make([]map[string]any, 0, len(projects))
	for _, p := range projects {
		issues, _ := s.q.ListIssues(ctx, generated.ListIssuesParams{ProjectID: p.ID, OrgID: org, Limit: 1000, Offset: 0})
		issueOut := make([]map[string]any, 0, len(issues))
		for _, i := range issues {
			issueOut = append(issueOut, map[string]any{
				"title": i.Title, "level": i.Level, "status": i.Status, "culprit": i.Culprit,
				"event_count": i.EventCount, "first_seen": i.FirstSeen.Time, "last_seen": i.LastSeen.Time,
				"first_release": i.FirstRelease,
			})
		}
		rules, _ := s.q.ListAlertRulesByProject(ctx, generated.ListAlertRulesByProjectParams{ProjectID: p.ID, OrgID: org})
		ruleOut := make([]map[string]any, 0, len(rules))
		for _, ru := range rules {
			ruleOut = append(ruleOut, map[string]any{"name": ru.Name, "type": ru.Type, "threshold": ru.Threshold, "window_minutes": ru.WindowMinutes})
		}
		releases, _ := s.q.ListReleasesByProject(ctx, generated.ListReleasesByProjectParams{ProjectID: p.ID, OrgID: org})
		relOut := make([]map[string]any, 0, len(releases))
		for _, rl := range releases {
			relOut = append(relOut, map[string]any{"version": rl.Version, "created_at": rl.CreatedAt.Time, "new_issues": rl.NewIssues})
		}
		projOut = append(projOut, map[string]any{
			"name": p.Name, "slug": p.Slug, "platform": p.Platform, "dsn": s.dsn(p.PublicKey, p.DsnID),
			"issues": issueOut, "alert_rules": ruleOut, "releases": relOut,
		})
	}

	s.audit(ctx, "data.export", orgRow.Name)
	w.Header().Set("Content-Disposition", "attachment; filename=flare-export.json")
	writeJSON(w, http.StatusOK, map[string]any{
		"exported_at": time.Now(),
		"org":         map[string]any{"name": orgRow.Name, "slug": orgRow.Slug, "created_at": orgRow.CreatedAt.Time},
		"members":     members,
		"channels":    chans,
		"projects":    projOut,
	})
}

// handleDeleteOrg permanently erases the whole workspace (right to erasure).
// Owner-only; cascades all org data and destroys the caller's session.
func (s *Server) handleDeleteOrg(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := s.q.DeleteOrg(ctx, orgIDFrom(ctx)); err != nil {
		slogError(w, "delete org", err)
		return
	}
	_ = s.sessions.Destroy(ctx)
	writeJSON(w, http.StatusNoContent, nil)
}
