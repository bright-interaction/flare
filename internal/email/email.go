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

const (
	// dialTimeout bounds connection setup, sendDeadline bounds the whole SMTP
	// conversation once connected. Both are mandatory: net/smtp applies NO
	// deadline of its own, so a server that accepts the TCP connection and then
	// stops reading parks the calling goroutine forever. Alert mail is sent from
	// the watchdog tick, so one stalled mail server used to wedge anomaly,
	// silence and monitor detection for the whole estate until a restart.
	dialTimeout  = 10 * time.Second
	sendDeadline = 30 * time.Second
)

// Send delivers a multipart (text + HTML) message to a single recipient.
// It always returns within roughly dialTimeout + sendDeadline.
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

	conn, err := m.dial(addr)
	if err != nil {
		return err
	}
	// One deadline for the entire exchange, set before any protocol chatter so
	// every subsequent read and write inherits it.
	if err := conn.SetDeadline(time.Now().Add(sendDeadline)); err != nil {
		conn.Close()
		return err
	}
	c, err := smtp.NewClient(conn, m.host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()

	if m.tlsMode == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: m.host}); err != nil {
				return err
			}
		}
	}
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

// dial opens the transport: implicit TLS (SMTPS, port 465) for "tls", plain TCP
// otherwise, with STARTTLS negotiated afterwards when the mode asks for it.
func (m *Mailer) dial(addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: dialTimeout}
	if m.tlsMode == "tls" {
		return tls.DialWithDialer(d, "tcp", addr, &tls.Config{ServerName: m.host})
	}
	return d.Dial("tcp", addr)
}

// stripHeaderCRLF removes CR and LF so an interpolated header value cannot inject
// additional SMTP headers or a message body (header injection).
func stripHeaderCRLF(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}

func (m *Mailer) build(to, subject, text, html string) []byte {
	boundary := "flare-boundary-9d7f3a1c"
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s <%s>\r\n", m.fromName, m.from)
	// Strip CR/LF from the recipient + subject so a value with an embedded newline cannot
	// inject extra SMTP headers or a body (defense-in-depth; callers use constant subjects
	// today, but header safety should not depend on that).
	fmt.Fprintf(&b, "To: %s\r\n", stripHeaderCRLF(to))
	fmt.Fprintf(&b, "Subject: %s\r\n", stripHeaderCRLF(subject))
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
