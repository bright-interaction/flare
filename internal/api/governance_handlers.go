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
		// Redact like every other read path. The export was returning the RAW
		// stored config, so a Slack or webhook URL (a bearer secret: whoever
		// holds it can post into the org's channel) was handed out in full to
		// anyone who could hit the export endpoint.
		chans = append(chans, map[string]any{
			"type": c.Type, "config": s.redactChannelConfig(c.Type, c.Config), "enabled": c.Enabled,
		})
	}

	// Data portability means ALL of it. The export used to take the first 1000
	// issues per project and silently drop the rest, and swallow any query
	// error, so a workspace with more than that got a quietly incomplete bundle
	// with nothing to say so. Page through instead, and record what happened.
	const exportPage = 1000
	var exportWarnings []string

	projOut := make([]map[string]any, 0, len(projects))
	for _, p := range projects {
		var issues []*generated.Issue
		for offset := int32(0); ; offset += exportPage {
			page, err := s.q.ListIssues(ctx, generated.ListIssuesParams{
				ProjectID: p.ID, OrgID: org, Limit: exportPage, Offset: offset,
			})
			if err != nil {
				exportWarnings = append(exportWarnings,
					"issues for project "+p.Slug+" are incomplete: "+err.Error())
				break
			}
			issues = append(issues, page...)
			if len(page) < exportPage {
				break
			}
		}
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
	out := map[string]any{
		"exported_at": time.Now(),
		"org":         map[string]any{"name": orgRow.Name, "slug": orgRow.Slug, "created_at": orgRow.CreatedAt.Time},
		"members":     members,
		"channels":    chans,
		"projects":    projOut,
		// Stated explicitly so a recipient knows what this bundle is and is not.
		"note": "Structured data only. Raw events, logs, spans and metrics are excluded by size; notification-channel secrets are redacted.",
	}
	if len(exportWarnings) > 0 {
		out["incomplete"] = true
		out["warnings"] = exportWarnings
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteOrg permanently erases the whole workspace (right to erasure).
// Owner-only; cascades all org data and destroys the caller's session.
func (s *Server) handleDeleteOrg(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	org := orgIDFrom(ctx)

	// Right-to-erasure must reach the raw telemetry. events/logs/spans are
	// partitioned hot tables with NO foreign key to projects/orgs, so the
	// ON DELETE CASCADE from orgs never touches them (this is exactly why
	// handleDeleteProject deletes them by hand). Delete every project's
	// telemetry first, then drop the org so its FK-linked rows (projects,
	// issues, source maps, releases, channels, keys, audit log) cascade.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		slogError(w, "delete org: begin tx", err)
		return
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)

	// Collect the member ids BEFORE the cascade removes the user rows: the
	// sessions table has no org column and no foreign key, so it is the one
	// store an org delete leaves fully intact unless it is cleared explicitly.
	members, err := qtx.ListUsersByOrg(ctx, org)
	if err != nil {
		slogError(w, "delete org: list members", err)
		return
	}
	userIDs := make([]string, 0, len(members))
	for _, m := range members {
		userIDs = append(userIDs, m.ID)
	}

	projects, err := qtx.ListProjectsByOrg(ctx, org)
	if err != nil {
		slogError(w, "delete org: list projects", err)
		return
	}
	for _, p := range projects {
		if err := qtx.DeleteProjectEvents(ctx, generated.DeleteProjectEventsParams{ProjectID: p.ID, OrgID: org}); err != nil {
			slogError(w, "delete org events", err)
			return
		}
		if err := qtx.DeleteProjectLogs(ctx, generated.DeleteProjectLogsParams{ProjectID: p.ID, OrgID: org}); err != nil {
			slogError(w, "delete org logs", err)
			return
		}
		if err := qtx.DeleteProjectSpans(ctx, generated.DeleteProjectSpansParams{ProjectID: p.ID, OrgID: org}); err != nil {
			slogError(w, "delete org spans", err)
			return
		}
		if err := qtx.DeleteProjectMetrics(ctx, generated.DeleteProjectMetricsParams{ProjectID: p.ID, OrgID: org}); err != nil {
			slogError(w, "delete org metrics", err)
			return
		}
	}
	if len(userIDs) > 0 {
		if err := qtx.DeleteSessionsForUsers(ctx, userIDs); err != nil {
			slogError(w, "delete org sessions", err)
			return
		}
	}
	if err := qtx.DeleteOrg(ctx, org); err != nil {
		slogError(w, "delete org", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		slogError(w, "delete org: commit", err)
		return
	}

	// Erase the org's telemetry from the Parquet cold tier too (aged-out data
	// the hot-tier delete cannot reach). This runs after the committed delete,
	// so it cannot be rolled back, but its OUTCOME must reach the caller: this
	// is a right-to-erasure request, and answering a bare 204 "done" while the
	// S3 cold tier still holds the org's telemetry (PurgeColdScope explicitly
	// does not support S3) is a compliance claim Flare cannot back.
	s.invalidateSecCaches(org)

	coldErr := ""
	if s.analytics != nil {
		if err := s.analytics.PurgeColdScope(ctx, "org_id", org); err != nil {
			slog.Error("cold-tier purge failed on org delete", "org", org, "err", err)
			coldErr = err.Error()
		}
	}

	if coldErr != "" {
		// Record BEFORE destroying the session, while the actor is still known.
		s.recordPartialErasure(ctx, org, "", "org_id", org, userIDFrom(ctx), coldErr)
	}
	_ = s.sessions.Destroy(ctx)
	if coldErr != "" {
		// 202, not 200. A client that only checks the status code reads 200 as
		// "erased" and moves on, which is the wrong thing to tell someone who
		// exercised a right to erasure. 202 says accepted-but-incomplete, and the
		// body says exactly what is outstanding.
		writeJSON(w, http.StatusAccepted, map[string]any{
			"status":         "partial",
			"hot_tier":       "erased",
			"cold_tier":      "NOT erased",
			"cold_tier_note": coldErr,
		})
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// recordPartialErasure persists an erasure obligation that was not fulfilled.
//
// It is written BEFORE the response so a crash between the two cannot lose it,
// and into a table with no foreign keys so it survives the deletion of the org
// and project it names. That is the whole reason it exists: for an org deletion
// there is no tenant left to notice, no audit row that outlives the org, and
// nobody to ask. Without this the obligation lived only in a log line.
func (s *Server) recordPartialErasure(ctx context.Context, org, project, column, value, by, note string) {
	if err := s.q.RecordPendingErasure(ctx, generated.RecordPendingErasureParams{
		ID:          id.New(),
		OrgID:       org,
		ProjectID:   pgtype.Text{String: project, Valid: project != ""},
		ScopeColumn: column,
		ScopeValue:  value,
		RequestedBy: pgtype.Text{String: by, Valid: by != ""},
		ColdTier:    "NOT erased",
		Reason:      note,
	}); err != nil {
		// Log loudly: at this point the data is still out there and the only
		// other record is this line.
		slog.Error("FAILED to record an unfulfilled erasure obligation",
			"org", org, "project", project, "scope", column+"="+value, "note", note, "err", err)
	}
}


// LogOpenErasures reports every unfulfilled erasure obligation. Called at
// startup so the obligation keeps announcing itself until an operator clears the
// row: the tenant who asked for it may no longer exist.
func (s *Server) LogOpenErasures(ctx context.Context) {
	rows, err := s.q.ListOpenErasures(ctx)
	if err != nil {
		slog.Warn("could not read pending erasures", "err", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	slog.Error("UNFULFILLED right-to-erasure obligations outstanding",
		"count", len(rows),
		"action", "erase the listed scopes from the object store, then set completed_at on the row")
	for _, r := range rows {
		slog.Error("  pending erasure",
			"requested_at", r.RequestedAt.Time.Format(time.RFC3339),
			"org", r.OrgID,
			"scope", r.ScopeColumn+"="+r.ScopeValue,
			"reason", r.Reason)
	}
}
