package api

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bright-interaction/flare/internal/ai"
	"github.com/bright-interaction/flare/internal/alerts"
	"github.com/bright-interaction/flare/internal/db/generated"
	"github.com/bright-interaction/flare/internal/id"
	"github.com/bright-interaction/flare/internal/ingest"
)

const (
	maxIngestBody       = 1 << 20 // 1 MiB compressed request body
	maxDecompressedBody = 8 << 20 // 8 MiB cap after gzip inflation (bomb guard)

	// maxEnvelopeItems caps the RECORDS in one envelope, which the byte caps
	// above do not.
	//
	// An envelope is newline-delimited, so 8 MiB of `{"type":"event"}\n{}\n`
	// pairs is ~400,000 items, and each event item runs UpsertIssue +
	// InsertEvent synchronously. One gzipped ~100 KiB POST therefore buys
	// hundreds of thousands of round trips for ONE rate-limit token, because the
	// limiter charges per REQUEST. That is the amplification: the attacker pays
	// once and the database pays 400,000 times.
	//
	// 1000 is far above any real SDK batch (Sentry clients flush a handful of
	// events per envelope, and a transaction carries its spans inside one item)
	// while capping the per-request work at something a single token can honestly
	// pay for.
	maxEnvelopeItems = 1000
)

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
	items, err := ingest.ParseEnvelope(body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid envelope")
		return
	}
	// Truncate rather than reject. A legitimate SDK never comes near this, and
	// answering 4xx would make Sentry clients DISCARD the payload as malformed;
	// accepting the first maxEnvelopeItems keeps real data flowing while the
	// amplification is bounded.
	dropped := 0
	if len(items) > maxEnvelopeItems {
		dropped = len(items) - maxEnvelopeItems
		items = items[:maxEnvelopeItems]
		slog.Warn("envelope truncated: too many items",
			"project_id", project.ID, "kept", maxEnvelopeItems, "dropped", dropped)
	}

	var lastID string
	var events, stored int
	for _, item := range items {
		switch item.Type {
		case "transaction":
			// Sentry-wire tracing: a transaction item carries the root span plus
			// its children. Map to Flare spans and store via the same path OTLP
			// traces use. Best-effort: a bad transaction never fails the ingest.
			spans, perr := ingest.ParseSentryTransaction(item.Payload)
			if perr != nil {
				slog.Warn("envelope transaction parse failed", "project_id", project.ID, "error", perr)
				continue
			}
			if perr := s.persistSpans(r.Context(), project, spans); perr != nil {
				slog.Warn("persist transaction spans failed", "project_id", project.ID, "error", perr)
			}
		case "event":
			events++
			eid, ierr := s.ingestOne(r.Context(), project, item.Payload)
			if ierr != nil {
				slog.Warn("envelope event ingest failed", "project_id", project.ID, "error", ierr)
				continue
			}
			stored++
			lastID = eid
		default:
			// sessions, attachments, client reports: not stored.
		}
	}
	// A 200 tells the SDK the payload was accepted, so it drops it. Answering
	// 200 when EVERY event in the envelope failed silently discarded the data.
	// 5xx, not 4xx: Sentry SDKs treat 4xx as "this payload is bad, discard it"
	// and only retry/back off on 5xx, and a total failure here is a server-side
	// problem, not a malformed request. Partial success still returns 200,
	// because the SDK cannot retry individual items.
	if events > 0 && stored == 0 {
		writeErr(w, http.StatusServiceUnavailable, "no event in the envelope could be stored")
		return
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

	// Auto-track the release this event reports (deploy markers can also
	// register one explicitly). Best-effort: a failure never blocks ingest.
	if ev.Release != "" {
		_ = s.q.UpsertRelease(ctx, generated.UpsertReleaseParams{
			ID: id.New(), ProjectID: project.ID, OrgID: project.OrgID, Version: ev.Release,
		})
	}

	issue, err := s.q.UpsertIssue(ctx, generated.UpsertIssueParams{
		ID:           id.New(),
		ProjectID:    project.ID,
		OrgID:        project.OrgID,
		Fingerprint:  ev.Fingerprint(),
		Title:        ev.Title,
		Culprit:      ev.Culprit,
		Level:        ev.Level,
		Platform:     ev.Platform,
		FirstRelease: ev.Release,
	})
	if err != nil {
		return "", err
	}

	// UpsertIssue leaves status untouched, so issue.Status is the PRIOR status.
	// A resolved issue that recurs is a regression: reopen it here.
	reopened := false
	if !issue.IsNew && issue.Status == "resolved" {
		if err := s.q.ReopenIssue(ctx, generated.ReopenIssueParams{ID: issue.ID, OrgID: project.OrgID}); err == nil {
			reopened = true
		}
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
		TraceID:        textOrNull(ev.TraceID),
		SpanID:         textOrNull(ev.SpanID),
	}); err != nil {
		return "", err
	}

	// Sensitive-data scan: flag the issue and surface a security signal when an
	// event leaked a secret / JWT / card number. Skipped for the security
	// project itself, both to avoid recursion and because its own event bodies
	// (key prefixes, IPs) are not leaks.
	sensitive := ""
	if project.Slug != securityProjectSlug {
		if kinds := detectSensitive(ev); len(kinds) > 0 {
			sensitive = strings.Join(kinds, ",")
			_ = s.q.MarkIssueSensitive(ctx, generated.MarkIssueSensitiveParams{
				ID: issue.ID, OrgID: project.OrgID, Sensitive: sensitive,
			})
			// The security event is itself re-ingested as an event into the
			// flare-security project, so its message must carry the SCRUBBED
			// title. Passing issue.Title raw copied the detected secret into a
			// project the detector deliberately skips, which round-tripped the
			// leak straight past the redaction.
			s.recordSecurityEvent(project.OrgID, "sensitive-data-in-payload",
				"possible "+sensitive+" in "+project.Name+" - issue: "+ai.Scrub(issue.Title), "")
		}
	}

	s.evaluateAlerts(project, issue, issue.IsNew, reopened, sensitive)

	// A newly-seen issue gets AI triage automatically (background, budget-gated),
	// so its root cause and fix are already waiting when a human or the AI
	// operator opens it - not a manual step after the fact.
	if issue.IsNew {
		s.maybeAutoTriage(project.OrgID, issue.ID)
	}
	return eventID, nil
}

// evaluateAlerts matches an ingested event against the project's enabled alert
// rules and dispatches at most one notification (the most actionable reason
// wins: regression > spike > new issue). Runs in the background with a detached
// context so it never blocks or fails ingest.
// pageableLevel reports whether an issue at this severity may fire an alert.
// info/debug are informational (heartbeats, breadcrumbs) and never page; every
// other level - including the empty/default, which Sentry treats as error -
// does. Kept a pure fn so the gate is unit-tested.
func pageableLevel(level string) bool {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "info", "debug":
		return false
	default:
		return true
	}
}

func (s *Server) evaluateAlerts(project *generated.Project, issue *generated.UpsertIssueRow, isNew, reopened bool, sensitive string) {
	// "Ignore" is the only control the UI offers for a noisy issue, and it hid
	// the issue from the list while its alerts kept firing, so the button did
	// not do the one thing users press it for. issue.Status is the PRIOR status
	// from UpsertIssue; a reopened issue was 'resolved', never 'ignored'.
	if issue.Status == "ignored" {
		return
	}
	// Low-severity signals (info/debug: heartbeats, breadcrumbs, health check-ins)
	// never page, whatever the rules say - they are informational, not incidents.
	// They still ingest and count toward the watchdog's silence-detection activity
	// baseline (CountEventsForProjectSince is level-agnostic), so a liveness
	// heartbeat can keep a project "active" without ever firing a new-issue/spike
	// alert. Warning+ (and the default/empty level) alert as before.
	if !pageableLevel(issue.Level) {
		return
	}
	s.goBackground("alert-eval", 15*time.Second, func(ctx context.Context) {
		rules, err := s.q.ListEnabledAlertRulesByProject(ctx, generated.ListEnabledAlertRulesByProjectParams{
			ProjectID: project.ID,
			OrgID:     project.OrgID,
		})
		if err != nil || len(rules) == 0 {
			return
		}
		// regression and new_issue are BOOLEAN rules: they either apply to this
		// event or they do not, so collapsing duplicates of those is harmless.
		byType := make(map[string]*generated.AlertRule, len(rules))
		// Spike rules are NOT interchangeable and must each be evaluated.
		//
		// Collapsing them to the "most sensitive" one silently disabled every
		// other spike rule on the project. moreSensitiveRule ranks on threshold
		// first, so "10 in 5min" beat "50 in 1440min" and the second never ran.
		// Those detect different things: a burst, and a slow burn that never
		// reaches 10 within any 5-minute window. An operator who configured both
		// got one, with nothing in the UI saying so.
		var spikes []*generated.AlertRule
		for _, r := range rules {
			if r.Type == "spike" {
				spikes = append(spikes, r)
				continue
			}
			if cur, ok := byType[r.Type]; !ok || moreSensitiveRule(r, cur) {
				byType[r.Type] = r
			}
		}
		// Deterministic order, most sensitive first, so the reason reported when
		// several rules fire at once is stable across runs and is the tightest
		// one that matched.
		sort.SliceStable(spikes, func(i, j int) bool { return moreSensitiveRule(spikes[i], spikes[j]) })

		var reason string
		switch {
		case reopened && byType["regression"] != nil:
			reason = "Regression"
		default:
			// First match wins. Dispatch stays at most one notification per
			// issue, so the existing "most actionable reason wins" contract holds
			// and the per-issue TrySetIssueSpike dedup still gates the send.
			for _, sr := range spikes {
				if reason = s.spikeReason(ctx, issue, project.OrgID, sr); reason != "" {
					break
				}
			}
		}
		if reason == "" && isNew && byType["new_issue"] != nil {
			reason = "New issue"
		}
		if reason == "" {
			return
		}

		chans, err := s.q.ListEnabledNotificationChannelsByOrg(ctx, project.OrgID)
		if err != nil || len(chans) == 0 {
			return
		}
		channels := make([]alerts.Channel, 0, len(chans))
		for _, c := range chans {
			channels = append(channels, alerts.Channel{ID: c.ID, OrgID: c.OrgID, Type: c.Type, Config: s.decryptChannelConfig(c.Type, c.Config)})
		}
		// When the detector flagged this issue, the title and culprit ARE the
		// leaked value. Alerts go out over email/Slack/webhooks, i.e. off-box to
		// third parties, so scrub before dispatch: otherwise the feature that
		// exists to catch a leaked secret is what forwards it.
		alertTitle, alertCulprit := issue.Title, issue.Culprit
		if sensitive != "" {
			alertTitle, alertCulprit = ai.Scrub(alertTitle), ai.Scrub(alertCulprit)
		}
		s.dispatcher.Dispatch(ctx, channels, alerts.Notification{
			ProjectName: project.Name,
			IssueID:     issue.ID,
			Title:       alertTitle,
			Level:       issue.Level,
			Culprit:     alertCulprit,
			EventCount:  issue.EventCount,
			Reason:      reason,
			URL:         strings.TrimRight(s.cfg.BaseURL, "/") + "/issues/" + issue.ID,
		})
	})
}

// moreSensitiveRule reports whether a would fire earlier than b: a lower
// threshold first, then a longer window at the same threshold. Kept pure so the
// tie-break is testable.
func moreSensitiveRule(a, b *generated.AlertRule) bool {
	if a.Threshold != b.Threshold {
		return a.Threshold < b.Threshold
	}
	return a.WindowMinutes > b.WindowMinutes
}

// spikeReason returns a non-empty reason when the issue has crossed the rule's
// threshold within its window AND this is the first spike alert for the issue
// in that window (atomic test-and-set dedup). Empty otherwise.
func (s *Server) spikeReason(ctx context.Context, issue *generated.UpsertIssueRow, org string, rule *generated.AlertRule) string {
	if rule.Threshold < 1 || rule.WindowMinutes < 1 {
		return ""
	}
	cutoff := pgtype.Timestamptz{Time: time.Now().Add(-time.Duration(rule.WindowMinutes) * time.Minute), Valid: true}
	cnt, err := s.q.CountEventsForIssueSince(ctx, generated.CountEventsForIssueSinceParams{
		IssueID: pgText(issue.ID), OrgID: org, ReceivedAt: cutoff,
	})
	if err != nil || cnt < int64(rule.Threshold) {
		return ""
	}
	rows, err := s.q.TrySetIssueSpike(ctx, generated.TrySetIssueSpikeParams{
		ID: issue.ID, OrgID: org, LastSpikeAt: cutoff,
	})
	if err != nil || rows == 0 {
		return ""
	}
	return fmt.Sprintf("Spike: %d events in %dm", cnt, rule.WindowMinutes)
}

// authIngest resolves the project from the DSN public key (the ingest secret)
// and sanity-checks the {projectID} path segment when present. The path id in a
// Sentry DSN is the numeric dsn_id; we also accept the cuid so native/Flare-key
// clients that use the primary key keep working. The public_key is the actual
// auth, so a mismatched-but-present path id is not by itself fatal.
func (s *Server) authIngest(w http.ResponseWriter, r *http.Request) (*generated.Project, bool) {
	key := ingestKey(r)
	if key == "" {
		slog.Warn("ingest rejected: missing key", "path", r.URL.Path, "remote", remoteIP(r))
		writeErr(w, http.StatusUnauthorized, "missing ingest key")
		return nil, false
	}
	project, err := s.q.GetProjectByPublicKey(r.Context(), key)
	if err != nil {
		// Log the key PREFIX only: enough to identify a stale/deleted DSN in
		// the field (the exact failure mode after a project deletion, which
		// used to be fully silent) without leaking a usable credential.
		slog.Warn("ingest rejected: unknown key", "key_prefix", keyPrefix(key), "path", r.URL.Path, "remote", remoteIP(r))
		s.recordSecurityEvent("", "ingest-auth-rejected", "unknown ingest key "+keyPrefix(key)+" at "+r.URL.Path, remoteIP(r))
		writeErr(w, http.StatusUnauthorized, "invalid ingest key")
		return nil, false
	}
	if pid := chi.URLParam(r, "projectID"); pid != "" && pid != project.DsnID && pid != project.ID {
		slog.Warn("ingest rejected: key/project mismatch", "key_prefix", keyPrefix(key), "path_project", pid, "remote", remoteIP(r))
		s.recordSecurityEvent(project.OrgID, "ingest-key-project-mismatch", "key "+keyPrefix(key)+" used against project "+pid, remoteIP(r))
		writeErr(w, http.StatusUnauthorized, "key does not match project")
		return nil, false
	}
	return project, true
}

// keyPrefix returns the first 8 chars of an ingest key for diagnostics.
func keyPrefix(key string) string {
	if len(key) > 8 {
		return key[:8]
	}
	return key
}

// remoteIP is the peer address without the port, for rejected-ingest logs.
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ingestKey pulls the public key from the X-Sentry-Auth header, the
// sentry_key query param, or the X-Flare-Key header.
// maxIngestKeyLen bounds a DSN public key. Real keys are id.New() cuids (~25
// chars); anything longer is not a key we could ever resolve.
const maxIngestKeyLen = 64

// plausibleIngestKey rejects anything that cannot be a DSN public key. The
// ingest rate limiter runs BEFORE authentication and buckets on this value, so
// without a shape check an attacker can mint unlimited distinct limiter keys
// (each an arbitrary-length header) and grow the limiter map without bound.
// Implausible values return "" so the request falls back to the IP bucket.
func plausibleIngestKey(k string) string {
	if k == "" || len(k) > maxIngestKeyLen {
		return ""
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return ""
		}
	}
	return k
}

func ingestKey(r *http.Request) string {
	return plausibleIngestKey(rawIngestKey(r))
}

func rawIngestKey(r *http.Request) string {
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
		// Cap the DECOMPRESSED size too: a 1 MiB gzip can inflate to gigabytes
		// (decompression bomb). Read one byte past the cap so overflow is caught.
		reader = io.LimitReader(gz, maxDecompressedBody+1)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "body too large")
		return nil, false
	}
	if len(body) > maxDecompressedBody {
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
