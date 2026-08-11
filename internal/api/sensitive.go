package api

import (
	"regexp"
	"sort"
	"strings"

	"github.com/bright-interaction/flare/internal/ai"
	"github.com/bright-interaction/flare/internal/ingest"
)

// Sensitive-data detection runs on ingest and flags an issue whose telemetry
// contains a value that should NEVER be there: an API secret, a JWT, or a
// payment card number. It is deliberately HIGH-PRECISION (secrets + Luhn-valid
// cards only, no emails/IPs/hashes) so the "sensitive" flag stays trustworthy
// and does not decorate every ordinary error. Emails/PII are noisier and left
// as a future opt-in.
var (
	reJWT    = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}`)
	reSecret = regexp.MustCompile(`\b(?:sk|pk|ghp|gho|ghs|xox[baprs]|AKIA)[-_A-Za-z0-9]{16,}\b`)
	// A run of 13-19 digits, allowing single spaces/dashes as card formatting.
	// luhnValid does the real filtering, so this stays loose on purpose.
	reCardCandidate = regexp.MustCompile(`[0-9](?:[ -]?[0-9]){12,18}`)
)

// detectSensitive returns the sorted, distinct sensitive-data kinds found in an
// event's high-signal text fields, or nil when clean.
func detectSensitive(ev ingest.NormalizedEvent) []string {
	var b strings.Builder
	for _, s := range []string{ev.Message, ev.ExceptionType, ev.ExceptionValue, ev.Culprit} {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	for _, f := range ev.Frames {
		b.WriteString(f.ContextLine)
		b.WriteByte('\n')
	}
	text := b.String()

	found := map[string]bool{}
	if reJWT.MatchString(text) {
		found["jwt"] = true
	}
	if reSecret.MatchString(text) {
		found["secret"] = true
	}
	for _, m := range reCardCandidate.FindAllString(text, -1) {
		if ai.IsPaymentCard(onlyDigits(m)) {
			found["card"] = true
			break
		}
	}
	if len(found) == 0 {
		return nil
	}
	kinds := make([]string, 0, len(found))
	for k := range found {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return kinds
}

// onlyDigits strips every non-digit from s.
//
// Card recognition itself lives in ai.IsPaymentCard, deliberately shared with
// the scrubber rather than reimplemented here. This file used to carry its own
// luhnValid, and the two copies had to agree for a flagged issue and a scrubbed
// title to tell the operator the same story. They agreed on being wrong: both
// gated on Luhn alone, which one in ten arbitrary digit runs passes.
func onlyDigits(s string) string {
	b := make([]byte, 0, len(s))
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b = append(b, byte(r))
		}
	}
	return string(b)
}
