// Package email sends transactional mail over SMTP (alert delivery, password
// reset). It is intentionally dependency-free (stdlib net/smtp + crypto/tls) so
// the engine stays a single static binary, and works against any SMTP server:
// a self-host mail server, mailhog in dev, or a provider's SMTP endpoint
// (Resend, SES) in production.
package email

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
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
	msg, err := m.build(to, subject, textBody, htmlBody)
	if err != nil {
		return err
	}
	addr := net.JoinHostPort(m.host, fmt.Sprintf("%d", m.port))

	var auth smtp.Auth
	if m.user != "" {
		auth = smtp.PlainAuth("", m.user, m.pass, m.host)
	}

	// Normalize once: the mode is operator-supplied config, so "STARTTLS" and
	// " starttls " must behave like "starttls".
	mode := strings.ToLower(strings.TrimSpace(m.tlsMode))
	if mode == "" {
		mode = "starttls"
	}

	conn, err := m.dial(mode, addr)
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

	// Opportunistic STARTTLS on every non-implicit-TLS mode, which is what the
	// previous smtp.SendMail path did. Gating it on the exact literal
	// "starttls" silently downgraded tlsMode "none" (and any other spelling) to
	// cleartext, including for the password-reset mail.
	encrypted := mode == "tls"
	if !encrypted {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: m.host}); err != nil {
				return err
			}
			encrypted = true
		} else if mode == "starttls" {
			// The operator asked for STARTTLS and the server does not offer it.
			// Failing is the only safe answer: silently continuing in cleartext
			// is how reset tokens end up on the wire.
			return fmt.Errorf("email: SMTP_TLS=starttls but %s does not advertise STARTTLS", m.host)
		}
	}
	if auth != nil {
		// net/smtp refuses PLAIN over an unencrypted connection for this exact
		// reason; keep that guarantee now that the client is built by hand.
		if !encrypted {
			return fmt.Errorf("email: refusing to send SMTP credentials over an unencrypted connection to %s", m.host)
		}
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
func (m *Mailer) dial(mode, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: dialTimeout}
	if mode == "tls" {
		return tls.DialWithDialer(d, "tcp", addr, &tls.Config{ServerName: m.host})
	}
	return d.Dial("tcp", addr)
}

// stripHeaderCRLF removes CR and LF so an interpolated header value cannot inject
// additional SMTP headers or a message body (header injection).
func stripHeaderCRLF(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}

// mimeBoundary mints a delimiter for one message.
//
// The old one was a compile-time constant, published in the open-source mirror.
// A part separator an attacker knows is a part separator an attacker can write:
// an exception value containing "\r\n--<boundary>\r\nContent-Type: text/html"
// added an attacker-authored MIME part to a message sent from the real Flare
// From: address, and anyone holding a project DSN key can put that string in an
// exception value. Dot-stuffing prevents SMTP command injection, so the blast
// radius was MIME parts rather than full message forgery, which is quite enough
// for a password-reset phishing part.
//
// Random per message, and the bodies are checked against it below, so even a
// collision cannot split the message.
func mimeBoundary() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// Fail closed rather than fall back to a predictable value: a caller
		// that cannot get entropy must not send a message whose part
		// separator an attacker can guess.
		return ""
	}
	return "flare-" + hex.EncodeToString(raw[:])
}

func (m *Mailer) build(to, subject, text, html string) ([]byte, error) {
	boundary := mimeBoundary()
	if boundary == "" {
		return nil, errors.New("mint mime boundary: no entropy available")
	}
	return m.buildWith(to, subject, text, html, boundary)
}

// buildWith is build with the boundary supplied, so the collision refusal can
// be tested. build itself cannot be: its boundary is random by design, so a
// test can never hand it a body containing that call's own separator.
func (m *Mailer) buildWith(to, subject, text, html, boundary string) ([]byte, error) {
	// A body containing the boundary would close the part early. With 128 bits
	// of entropy this cannot happen in practice; the cost of checking is one
	// scan and the failure it covers is total.
	if strings.Contains(text, boundary) || strings.Contains(html, boundary) {
		return nil, errors.New("message body collides with its own mime boundary")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s <%s>\r\n", stripHeaderCRLF(m.fromName), stripHeaderCRLF(m.from))
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
	return []byte(b.String()), nil
}
