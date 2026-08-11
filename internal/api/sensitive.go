package api

import (
	"strings"

	"github.com/bright-interaction/flare/internal/ingest"
	"github.com/bright-interaction/flare/internal/scan"
)

// Sensitive-data detection runs on ingest and flags an issue whose telemetry
// contains a value that should NEVER be there.
//
// There is deliberately NO regex in this file. Every shape it can report comes
// from internal/scan, the same table the scrubber rewrites with, because this
// file used to keep its own copies and they drifted: the scrubber learned six
// more token families and a lower minimum length, the detector did not, and a
// leaked GitLab token was scrubbed on its way to the model and served in plain
// text to the dashboard with no badge, no security event and no alert. The
// customer never learned to rotate it.
//
// scan.Kinds reports only the high-confidence kinds (secrets, JWTs, cards,
// private keys, personnummer). Emails and IP addresses are scrubbed but not
// flagged: the flag has to stay trustworthy, and it also raises a security
// event and seeds an alert.

// sensitiveText is the ONE declared list of an event's payload-text fields.
// The detector scans it and the redactors rewrite it, so a field cannot be
// scanned without also being redacted. That gap was the audit's only CRITICAL:
// detectSensitive read frame context lines, the REST response scrubbed exactly
// two fields, and the single most common way the flag fires (a JWT in a source
// context line) produced a flagged issue whose flagged value was served raw by
// the endpoint the flag exists to protect.
func sensitiveText(ev ingest.NormalizedEvent) string {
	var b strings.Builder
	for _, s := range []string{ev.Message, ev.ExceptionType, ev.ExceptionValue, ev.Culprit} {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	for _, f := range ev.Frames {
		b.WriteString(f.ContextLine)
		b.WriteByte('\n')
	}
	return b.String()
}

// detectSensitive returns the sorted, distinct sensitive-data kinds found in an
// event's high-signal text fields, or nil when clean.
func detectSensitive(ev ingest.NormalizedEvent) []string {
	return scan.Kinds(sensitiveText(ev))
}
