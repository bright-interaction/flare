package ai

import "github.com/bright-interaction/flare/internal/scan"

// Scrub removes common PII/secret shapes from text before it is sent to the
// model - the sovereign guarantee: raw personal data and credentials never
// leave the tenant's boundary. Code structure (file names, functions, line
// numbers) is preserved so triage stays useful.
//
// The rules live in internal/scan, NOT here. They used to live here, and the
// ingest-time detector in internal/api kept a second copy of the same
// prefixed-token regex; this one gained six token families and dropped its
// minimum length from 16 to 10, and that one did not. A GitLab token was
// therefore redacted on its way to a model and stored in plain text in the
// dashboard with no flag raised. One table, two entry points, no hand-syncing.
func Scrub(s string) string { return scan.Text(s) }

// ScrubVersion scrubs an identifier-shaped value (a release, a version, an
// environment). Credential and personal-data rules apply; the generic
// form rules do not, because a release in this estate is a git SHA and the
// 40-hex rule turned every one of them into "[hash]".
func ScrubVersion(s string) string { return scan.Version(s) }

// ScrubJSON scrubs a JSON document by walking it, rewriting its leaves and its
// keys, and re-marshalling. Always returns valid JSON.
func ScrubJSON(raw []byte) []byte { return scan.JSON(raw) }

// IsPaymentCard reports whether a digits-only string is plausibly a real
// payment card. See internal/scan/card.go for why Luhn alone is not enough.
func IsPaymentCard(digits string) bool { return scan.IsPaymentCard(digits) }

// TextHasPaymentCard reports whether text contains a payment card number,
// sharing the candidate search as well as the decision.
func TextHasPaymentCard(text string) bool { return scan.TextHasPaymentCard(text) }
