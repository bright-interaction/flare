package api

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bright-interaction/flare/internal/alerts"
	"github.com/bright-interaction/flare/internal/db/generated"
	"github.com/bright-interaction/flare/internal/id"
	"github.com/bright-interaction/flare/internal/ingest"
)

const maxIngestBody = 1 << 20 // 1 MiB

// handleEnvelope ingests a Sentry envelope (the modern @sentry/* SDK path).
func (s *Server) handleEnvelope(w http.ResponseWriter, r *http.Request) {
	project, ok := s.authIngest(w, r)
	if !ok {
		return
	}
	body, ok := readIngestBody(w, r)
	if !ok {
		return
	}
	payloads, err := ingest.ParseEnvelope(body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid envelope")
		return
	}
	var lastID string
	for _, p := range payloads {
		eid, err := s.ingestOne(r.Context(), project, p)
		if err != nil {
			slog.Warn("envelope event ingest failed", "project_id", project.ID, "error", err)
			continue
		}
		lastID = eid
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": lastID})
}

// handleStore ingests a single event JSON (Sentry /store/ and Flare's native
// /events endpoint).
func (s *Server) handleStore(w http.ResponseWriter, r *http.Request) {
	project, ok := s.authIngest(w, r)
	if !ok {
		return
	}
	body, ok := readIngestBody(w, r)
	if !ok {
		return
	}
	eid, err := s.ingestOne(r.Context(), project, body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid event")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": eid})
}

// ingestOne normalizes, groups, and persists one event. Returns the event id.
func (s *Server) ingestOne(ctx context.Context, project *generated.Project, raw []byte) (string, error) {
	ev, err := ingest.ParseEvent(raw)
	if err != nil {
		return "", err
	}
	eventID := ev.EventID
	if eventID == "" {
		eventID = id.New()
	}

	issue, err := s.q.UpsertIssue(ctx, generated.UpsertIssueParams{
		ID:          id.New(),
		ProjectID:   project.ID,
		OrgID:       project.OrgID,
		Fingerprint: ev.Fingerprint(),
		Title:       ev.Title,
		Culprit:     ev.Culprit,
		Level:       ev.Level,
		Platform:    ev.Platform,
	})
	if err != nil {
		return "", err
	}

	if err := s.q.InsertEvent(ctx, generated.InsertEventParams{
		ID:             id.New(),
		ProjectID:      project.ID,
		OrgID:          project.OrgID,
		IssueID:        pgText(issue.ID),
		Level:          ev.Level,
		Message:        ev.Message,
		ExceptionType:  ev.ExceptionType,
		ExceptionValue: ev.ExceptionValue,
		Platform:       ev.Platform,
		Environment:    ev.Environment,
		Release:        ev.Release,
		Stacktrace:     stacktraceJSON(ev.Frames),
		Payload:        ev.Raw,
	}); err != nil {
		return "", err
	}

	if issue.IsNew {
		s.fireNewIssueAlert(project, issue)
	}
	return eventID, nil
}

// fireNewIssueAlert dispatches in the background with a detached context so it
// never blocks or fails the ingest request.
func (s *Server) fireNewIssueAlert(project *generated.Project, issue *generated.UpsertIssueRow) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		rules, err := s.q.ListEnabledAlertRulesByProject(ctx, generated.ListEnabledAlertRulesByProjectParams{
			ProjectID: project.ID,
			OrgID:     project.OrgID,
			Type:      "new_issue",
		})
		if err != nil || len(rules) == 0 {
			return
		}
		chans, err := s.q.ListEnabledNotificationChannelsByOrg(ctx, project.OrgID)
		if err != nil || len(chans) == 0 {
			return
		}
		channels := make([]alerts.Channel, 0, len(chans))
		for _, c := range chans {
			channels = append(channels, alerts.Channel{Type: c.Type, Config: c.Config})
		}
		s.dispatcher.Dispatch(ctx, channels, alerts.Notification{
			ProjectName: project.Name,
			IssueID:     issue.ID,
			Title:       issue.Title,
			Level:       issue.Level,
			Culprit:     issue.Culprit,
			EventCount:  issue.EventCount,
			URL:         strings.TrimRight(s.cfg.BaseURL, "/") + "/issues/" + issue.ID,
		})
	}()
}

// authIngest resolves the project from the DSN public key (the ingest secret)
// and sanity-checks the {projectID} path segment when present. The path id in a
// Sentry DSN is the numeric dsn_id; we also accept the cuid so native/Flare-key
// clients that use the primary key keep working. The public_key is the actual
// auth, so a mismatched-but-present path id is not by itself fatal.
func (s *Server) authIngest(w http.ResponseWriter, r *http.Request) (*generated.Project, bool) {
	key := ingestKey(r)
	if key == "" {
		writeErr(w, http.StatusUnauthorized, "missing ingest key")
		return nil, false
	}
	project, err := s.q.GetProjectByPublicKey(r.Context(), key)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid ingest key")
		return nil, false
	}
	if pid := chi.URLParam(r, "projectID"); pid != "" && pid != project.DsnID && pid != project.ID {
		writeErr(w, http.StatusUnauthorized, "key does not match project")
		return nil, false
	}
	return project, true
}

// ingestKey pulls the public key from the X-Sentry-Auth header, the
// sentry_key query param, or the X-Flare-Key header.
func ingestKey(r *http.Request) string {
	if h := r.Header.Get("X-Sentry-Auth"); h != "" {
		// Header looks like: "Sentry sentry_key=KEY, sentry_version=7, ...".
		// Split on both spaces and commas so the leading scheme word and the
		// comma-separated pairs are all individual tokens.
		fields := strings.FieldsFunc(h, func(c rune) bool { return c == ',' || c == ' ' })
		for _, f := range fields {
			if v, ok := strings.CutPrefix(f, "sentry_key="); ok {
				return strings.TrimSpace(v)
			}
		}
	}
	if k := r.URL.Query().Get("sentry_key"); k != "" {
		return k
	}
	return strings.TrimSpace(r.Header.Get("X-Flare-Key"))
}

func readIngestBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	var reader io.Reader = http.MaxBytesReader(w, r.Body, maxIngestBody)
	if strings.Contains(r.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(reader)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid gzip body")
			return nil, false
		}
		defer gz.Close()
		reader = gz
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "body too large")
		return nil, false
	}
	return body, true
}

func stacktraceJSON(frames []ingest.Frame) json.RawMessage {
	if len(frames) == 0 {
		return nil
	}
	b, err := json.Marshal(map[string]any{"frames": frames})
	if err != nil {
		return nil
	}
	return b
}
