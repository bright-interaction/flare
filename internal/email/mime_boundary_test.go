package email

import (
	"strings"
	"testing"
)

// M8. The MIME boundary was a compile-time constant published in the
// open-source mirror. A part separator an attacker knows is a part separator an
// attacker can write, and the alert body interpolates the exception value raw,
// so anyone holding a project DSN key could add their own MIME part to a
// message sent from the real Flare From: address. Dot-stuffing keeps the blast
// radius at MIME parts rather than message forgery, which is quite enough for a
// password-reset phishing part rendered by most clients.
func TestMimeBoundaryIsPerMessage(t *testing.T) {
	m := &Mailer{host: "localhost", port: 25, from: "flare@example.com", fromName: "Flare"}

	a, err := m.build("to@example.com", "subject", "text", "<p>html</p>")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	b, err := m.build("to@example.com", "subject", "text", "<p>html</p>")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	ba, bb := boundaryOf(t, string(a)), boundaryOf(t, string(b))
	if ba == bb {
		t.Fatalf("two messages share a boundary (%q), so it is guessable", ba)
	}
	if strings.Contains(ba, "9d7f3a1c") {
		t.Fatalf("the published constant boundary is still in use: %q", ba)
	}

	// A body that manages to contain the boundary would close the part early,
	// so the message is refused rather than sent split.
	if out, berr := m.buildWith("to@example.com", "s", "boom\r\n--fixed\r\n", "<p>x</p>", "fixed"); berr == nil {
		t.Fatalf("a body containing the boundary was accepted:\n%s", out)
	}
}

// An injected MIME part must not survive into the message.
func TestInjectedMimePartDoesNotSplitTheMessage(t *testing.T) {
	m := &Mailer{host: "localhost", port: 25, from: "flare@example.com", fromName: "Flare"}
	body := "boom\r\n--flare-boundary-9d7f3a1c\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n" +
		`<h1>Your Flare password expired</h1><a href="https://evil.example">Reset now</a>` + "\r\n"
	raw, err := m.build("to@example.com", "Alert", body, "<p>ok</p>")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	out := string(raw)
	b := boundaryOf(t, out)
	// The attacker's separator is not this message's separator, so it is inert
	// text inside the text/plain part rather than a part header.
	if strings.Count(out, "--"+b) != 3 { // two openers plus the closer
		t.Fatalf("message has %d real part markers, want 3:\n%s", strings.Count(out, "--"+b), out)
	}
}

func boundaryOf(t *testing.T, msg string) string {
	t.Helper()
	const marker = `boundary="`
	i := strings.Index(msg, marker)
	if i < 0 {
		t.Fatalf("no boundary declared:\n%s", msg)
	}
	rest := msg[i+len(marker):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		t.Fatalf("unterminated boundary:\n%s", msg)
	}
	return rest[:j]
}
