// Package alerts dispatches issue notifications to configured channels. Phase
// 2 ships a log sink and an SSRF-guarded webhook; richer channel types
// (Telegram, Slack, Cloud routing) plug in behind the same Dispatch call.
package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/http"
	"syscall"
	"time"

	"github.com/bright-interaction/flare/internal/email"
)

// Channel is a destination for notifications, mapped from a stored
// notification_channels row.
type Channel struct {
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
// logged, never propagated: alerting must not block or fail ingest.
func (d *Dispatcher) Dispatch(ctx context.Context, channels []Channel, n Notification) {
	for _, ch := range channels {
		switch ch.Type {
		case "webhook":
			d.webhook(ctx, ch.Config, n)
		case "email":
			d.emailAlert(ch.Config, n)
		case "log":
			slog.Info("flare alert", "reason", n.Reason, "title", n.Title, "level", n.Level, "events", n.EventCount, "url", n.URL)
		default:
			slog.Warn("alert channel type not supported", "type", ch.Type)
		}
	}
}

// emailAlert renders and sends a new-issue notification to an email channel's
// recipient. Best-effort: a misconfigured channel or unconfigured mailer logs
// and returns, never blocking ingest.
func (d *Dispatcher) emailAlert(cfgRaw json.RawMessage, n Notification) {
	if !d.mailer.Enabled() {
		slog.Warn("email alert skipped: SMTP not configured")
		return
	}
	var cfg struct {
		To string `json:"to"`
	}
	if err := json.Unmarshal(cfgRaw, &cfg); err != nil || cfg.To == "" {
		slog.Warn("email channel misconfigured")
		return
	}
	reason := n.Reason
	if reason == "" {
		reason = "Alert"
	}
	subject := fmt.Sprintf("[Flare] %s in %s: %s", reason, n.ProjectName, n.Title)
	text := fmt.Sprintf("%s in %s\n\n%s\n%s\nEvents: %d\n\nView: %s\n",
		reason, n.ProjectName, n.Title, n.Culprit, n.EventCount, n.URL)
	body := alertHTML(n, reason)
	if err := d.mailer.Send(cfg.To, subject, body, text); err != nil {
		slog.Warn("email alert delivery failed", "error", err)
	}
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

func (d *Dispatcher) webhook(ctx context.Context, cfgRaw json.RawMessage, n Notification) {
	var cfg struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(cfgRaw, &cfg); err != nil || cfg.URL == "" {
		slog.Warn("webhook channel misconfigured")
		return
	}
	body, _ := json.Marshal(n)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		slog.Warn("webhook build request", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		slog.Warn("webhook delivery failed", "error", err)
		return
	}
	_ = resp.Body.Close()
}

func isBlocked(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
}
