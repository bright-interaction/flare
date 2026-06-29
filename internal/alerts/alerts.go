// Package alerts dispatches issue notifications to configured channels. Phase
// 2 ships a log sink and an SSRF-guarded webhook; richer channel types
// (Telegram, Slack, Cloud routing) plug in behind the same Dispatch call.
package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"syscall"
	"time"
)

// Channel is a destination for notifications, mapped from a stored
// notification_channels row.
type Channel struct {
	Type   string
	Config json.RawMessage
}

// Notification is the payload describing the issue that fired the alert.
type Notification struct {
	ProjectName string `json:"project"`
	IssueID     string `json:"issue_id"`
	Title       string `json:"title"`
	Level       string `json:"level"`
	Culprit     string `json:"culprit"`
	EventCount  int64  `json:"events"`
	URL         string `json:"url"`
}

type Dispatcher struct {
	client *http.Client
}

func NewDispatcher() *Dispatcher {
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
		case "log":
			slog.Info("flare alert", "title", n.Title, "level", n.Level, "events", n.EventCount, "url", n.URL)
		default:
			slog.Warn("alert channel type not supported", "type", ch.Type)
		}
	}
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
