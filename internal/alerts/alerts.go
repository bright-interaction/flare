// Package alerts dispatches issue notifications to configured channels. Phase
// 2 ships a log sink and an SSRF-guarded webhook; richer channel types
// (Telegram, Slack, webhook routing) plug in behind the same Dispatch call.
package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bright-interaction/flare/internal/netguard"
	"html"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/bright-interaction/flare/internal/email"
)

// Channel is a destination for notifications, mapped from a stored
// notification_channels row. ID/OrgID identify the row so per-channel delivery
// status can be recorded (org-scoped); both may be empty for ad-hoc dispatch.
type Channel struct {
	ID     string
	OrgID  string
	Type   string
	Config json.RawMessage
}

// Notification is the payload describing the issue that fired the alert.
// Reason names what triggered it ("New issue", "Regression", "Spike: N events
// in Mm") and drives the subject/headline.
type Notification struct {
	ProjectName string `json:"project"`
	IssueID     string `json:"issue_id"`
	Title       string `json:"title"`
	Level       string `json:"level"`
	Culprit     string `json:"culprit"`
	EventCount  int64  `json:"events"`
	Reason      string `json:"reason"`
	URL         string `json:"url"`
}

type Dispatcher struct {
	client *http.Client
	mailer *email.Mailer
	// Recorder, when set, persists the outcome of each delivery attempt (nil
	// err = success). Wired by the server to the notification_channels delivery
	// columns so a silently-failing channel becomes visible. Kept as a hook so
	// the alerts package stays free of any DB dependency.
	Recorder func(ctx context.Context, orgID, channelID string, err error)
}

func NewDispatcher(mailer *email.Mailer) *Dispatcher {
	// SSRF guard: the Control hook runs after DNS resolution with the real
	// dialed IP, so it blocks loopback/private/link-local targets even under
	// DNS rebinding. Redirects are disabled.
	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil || isBlocked(ip) {
				return errors.New("blocked destination address")
			}
			return nil
		},
	}
	return &Dispatcher{
		mailer: mailer,
		client: &http.Client{
			Timeout:   10 * time.Second,
			Transport: &http.Transport{DialContext: dialer.DialContext},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("redirects disabled")
			},
		},
	}
}

// Dispatch sends the notification to every channel, best-effort. Failures are
// recorded and logged, never propagated: alerting must not block or fail ingest.
func (d *Dispatcher) Dispatch(ctx context.Context, channels []Channel, n Notification) {
	for _, ch := range channels {
		err := d.send(ctx, ch, n)
		d.record(ctx, ch, err)
		if err != nil {
			slog.Warn("alert delivery failed", "type", ch.Type, "error", err)
		}
	}
}

// DispatchOne sends to a single channel and RETURNS the delivery result, so the
// "send test" operator action can show the real HTTP/SMTP outcome. The outcome
// is also recorded, so a test doubles as a live health check of the channel.
func (d *Dispatcher) DispatchOne(ctx context.Context, ch Channel, n Notification) error {
	err := d.send(ctx, ch, n)
	d.record(ctx, ch, err)
	return err
}

// send delivers to one channel and returns the delivery error (nil on success).
func (d *Dispatcher) send(ctx context.Context, ch Channel, n Notification) error {
	switch ch.Type {
	case "webhook":
		return d.webhook(ctx, ch.Config, n)
	case "slack":
		return d.slack(ctx, ch.Config, n)
	case "email":
		return d.emailAlert(ch.Config, n)
	case "log":
		slog.Info("flare alert", "reason", n.Reason, "title", n.Title, "level", n.Level, "events", n.EventCount, "url", n.URL)
		return nil
	default:
		return fmt.Errorf("unsupported channel type %q", ch.Type)
	}
}

// record persists a delivery outcome when a recorder is wired and the channel
// has a stable id + org (ad-hoc dispatch may pass empty ids).
func (d *Dispatcher) record(ctx context.Context, ch Channel, err error) {
	if d.Recorder != nil && ch.ID != "" && ch.OrgID != "" {
		d.Recorder(ctx, ch.OrgID, ch.ID, err)
	}
}

// slack posts a Block Kit message to an incoming-webhook URL. Reuses the
// SSRF-guarded client (hooks.slack.com is public, loopback/private blocked).
func (d *Dispatcher) slack(ctx context.Context, cfgRaw json.RawMessage, n Notification) error {
	var cfg struct {
		WebhookURL string `json:"webhook_url"`
	}
	if err := json.Unmarshal(cfgRaw, &cfg); err != nil || cfg.WebhookURL == "" {
		return errors.New("slack channel misconfigured (missing webhook_url)")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.WebhookURL, bytes.NewReader(slackPayload(n)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("slack webhook returned status %d", resp.StatusCode)
	}
	return nil
}

// slackPayload renders the notification as a Slack Block Kit message.
func slackPayload(n Notification) []byte {
	reason := n.Reason
	if reason == "" {
		reason = "Alert"
	}
	meta := fmt.Sprintf("%s · %d event(s)", n.Level, n.EventCount)
	if n.Culprit != "" {
		meta += " · " + n.Culprit
	}
	blocks := []map[string]any{
		{"type": "section", "text": map[string]any{
			"type": "mrkdwn",
			"text": fmt.Sprintf("*%s* in *%s*\n<%s|%s>", slackEsc(reason), slackEsc(n.ProjectName), n.URL, slackEsc(n.Title)),
		}},
		{"type": "context", "elements": []map[string]any{
			{"type": "mrkdwn", "text": slackEsc(meta)},
		}},
	}
	b, _ := json.Marshal(map[string]any{
		"text":   fmt.Sprintf("[Flare] %s: %s", reason, n.Title),
		"blocks": blocks,
	})
	return b
}

func slackEsc(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	return strings.ReplaceAll(s, ">", "&gt;")
}

// emailAlert renders and sends a new-issue notification to an email channel's
// recipient. Best-effort: a misconfigured channel or unconfigured mailer logs
// and returns, never blocking ingest.
func (d *Dispatcher) emailAlert(cfgRaw json.RawMessage, n Notification) error {
	if !d.mailer.Enabled() {
		return errors.New("email delivery not configured on this server (SMTP unset)")
	}
	var cfg struct {
		To string `json:"to"`
	}
	if err := json.Unmarshal(cfgRaw, &cfg); err != nil || cfg.To == "" {
		return errors.New("email channel misconfigured (missing to)")
	}
	reason := n.Reason
	if reason == "" {
		reason = "Alert"
	}
	subject := fmt.Sprintf("[Flare] %s in %s: %s", reason, n.ProjectName, n.Title)
	text := fmt.Sprintf("%s in %s\n\n%s\n%s\nEvents: %d\n\nView: %s\n",
		reason, n.ProjectName, n.Title, n.Culprit, n.EventCount, n.URL)
	body := alertHTML(n, reason)
	return d.mailer.Send(cfg.To, subject, body, text)
}

func alertHTML(n Notification, reason string) string {
	esc := html.EscapeString
	culprit := ""
	if n.Culprit != "" {
		culprit = fmt.Sprintf(`<p style="margin:4px 0;color:#71717a;font:13px/1.5 ui-monospace,monospace">%s</p>`, esc(n.Culprit))
	}
	return fmt.Sprintf(`<div style="font:15px/1.6 -apple-system,Segoe UI,sans-serif;color:#18181b;max-width:560px">
<p style="margin:0 0 4px;font-size:12px;letter-spacing:.04em;text-transform:uppercase;color:#a16207">%s</p>
<h2 style="margin:0 0 2px;font-size:18px">%s</h2>
%s
<p style="margin:12px 0;color:#52525b">Project <strong>%s</strong> &middot; %d event(s) &middot; %s</p>
<p style="margin:16px 0"><a href="%s" style="display:inline-block;background:#f59e0b;color:#18181b;text-decoration:none;padding:9px 16px;border-radius:6px;font-weight:600">View issue</a></p>
</div>`, esc(reason), esc(n.Title), culprit, esc(n.ProjectName), n.EventCount, esc(n.Level), esc(n.URL))
}

func (d *Dispatcher) webhook(ctx context.Context, cfgRaw json.RawMessage, n Notification) error {
	var cfg struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(cfgRaw, &cfg); err != nil || cfg.URL == "" {
		return errors.New("webhook channel misconfigured (missing url)")
	}
	body, _ := json.Marshal(n)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}
	return nil
}

func isBlocked(ip net.IP) bool {
	return netguard.IsBlockedIP(ip)
}
