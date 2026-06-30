// Package email sends transactional mail over SMTP (alert delivery, password
// reset). It is intentionally dependency-free (stdlib net/smtp + crypto/tls) so
// the engine stays a single static binary, and works against any SMTP server:
// a self-host mail server, mailhog in dev, or a provider's SMTP endpoint
// (Resend, SES) in production.
package email

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// Mailer holds resolved SMTP settings. The zero value (and any Mailer whose
// host/from is empty) is disabled: Send returns ErrDisabled so callers can
// no-op cleanly on a self-host without mail configured.
type Mailer struct {
	host     string
	port     int
	user     string
	pass     string
	from     string
	fromName string
	tlsMode  string // "tls" | "starttls" | "none"
}

// ErrDisabled is returned by Send when no SMTP host/from is configured.
var ErrDisabled = fmt.Errorf("email: SMTP not configured")

// New builds a Mailer from resolved config values.
func New(host string, port int, user, pass, from, fromName, tlsMode string) *Mailer {
	if tlsMode == "" {
		tlsMode = "starttls"
	}
	if fromName == "" {
		fromName = "Flare"
	}
	return &Mailer{host: host, port: port, user: user, pass: pass, from: from, fromName: fromName, tlsMode: tlsMode}
}

// Enabled reports whether the mailer can send.
func (m *Mailer) Enabled() bool { return m != nil && m.host != "" && m.from != "" }

// Send delivers a multipart (text + HTML) message to a single recipient.
func (m *Mailer) Send(to, subject, htmlBody, textBody string) error {
	if !m.Enabled() {
		return ErrDisabled
	}
	msg := m.build(to, subject, textBody, htmlBody)
	addr := net.JoinHostPort(m.host, fmt.Sprintf("%d", m.port))

	var auth smtp.Auth
	if m.user != "" {
		auth = smtp.PlainAuth("", m.user, m.pass, m.host)
	}

	switch m.tlsMode {
	case "tls":
		return m.sendImplicitTLS(addr, auth, to, msg)
	default: // "starttls" and "none": SendMail upgrades via STARTTLS when offered.
		return smtp.SendMail(addr, auth, m.from, []string{to}, msg)
	}
}

// sendImplicitTLS dials a TLS socket first (SMTPS, port 465).
func (m *Mailer) sendImplicitTLS(addr string, auth smtp.Auth, to string, msg []byte) error {
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", addr, &tls.Config{ServerName: m.host})
	if err != nil {
		return err
	}
	c, err := smtp.NewClient(conn, m.host)
	if err != nil {
		return err
	}
	defer c.Close()
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return err
		}
	}
	if err := c.Mail(m.from); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	wc, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := wc.Write(msg); err != nil {
		return err
	}
	if err := wc.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func (m *Mailer) build(to, subject, text, html string) []byte {
	boundary := "flare-boundary-9d7f3a1c"
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s <%s>\r\n", m.fromName, m.from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	b.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)

	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	b.WriteString(text)
	b.WriteString("\r\n\r\n")

	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
	b.WriteString(html)
	b.WriteString("\r\n\r\n")

	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return []byte(b.String())
}
