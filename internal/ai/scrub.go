package ai

import (
	"regexp"
	"strings"
)

// Scrub removes common PII/secret shapes from text before it is sent to the
// model - the sovereign guarantee: raw personal data and credentials never
// leave the tenant's boundary. Code structure (file names, functions, line
// numbers) is preserved so triage stays useful.

// High-confidence structured secrets, run first via dedicated passes.
var (
	// PEM private-key blocks of any type (RSA, EC, OPENSSH, plain).
	rePEM = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	// Credentials embedded in a URL: scheme://user:PASSWORD@host -> redact the password.
	reURLCreds = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://[^:@/\s]+:)[^@/\s]+(@)`)
	// key=value / key: value / "key":"value" where the key names a secret. The
	// key is matched as a whole identifier that CONTAINS a secret word, so
	// snake_case names like aws_secret_access_key (no \b around the inner words)
	// are caught. This scrubs credentials that carry no recognizable prefix (DB
	// passwords, AWS secret access keys, generic API keys) by their context.
	reAssignSecret = regexp.MustCompile(`(?i)([A-Za-z0-9_.\-]*(?:passphrase|password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|client[_-]?secret|auth[_-]?token|refresh[_-]?token|session[_-]?key|apikey)[A-Za-z0-9_.\-]*)(\s*["']?\s*[:=]\s*["']?)([^\s"',;)}<>]{6,})`)
	// Authorization: Bearer <token> and Basic <b64>.
	reBearer = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._+/=\-]{8,}`)
	// Formatted or bare payment card: 13-19 digits with optional single space or
	// dash between groups. Luhn-validated before replacement so ordinary long
	// numbers (ids, timestamps) are left intact.
	reCardCandidate = regexp.MustCompile(`\b\d(?:[ -]?\d){12,18}\b`)
)

var scrubbers = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`eyJ[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}`), "[jwt]"},
	// Prefixed tokens: Stripe/OpenAI (sk/pk), GitHub (ghp/ghs/gho/ghu/ghr/github_pat),
	// Slack (xox*/xapp), AWS access-key id (AKIA/ASIA), GitLab (glpat).
	{regexp.MustCompile(`\b(?:sk|pk|ghp|ghs|gho|ghu|ghr|github_pat|glpat|xox[baprs]|xapp|AKIA|ASIA)[-_A-Za-z0-9]{10,}\b`), "[secret]"},
	// Google API key.
	{regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`), "[secret]"},
	{regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`), "[email]"},
	{regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`), "[ip]"},
	{regexp.MustCompile(`\b(?:[0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}\b`), "[mac]"},
	{regexp.MustCompile(`\b[A-Fa-f0-9]{40,}\b`), "[hash]"},
	// Any remaining bare long digit run (unformatted, non-card).
	{regexp.MustCompile(`\b\d{13,19}\b`), "[number]"},
}

// Scrub returns text with PII/secret shapes replaced by placeholders.
func Scrub(s string) string {
	// Structured, high-confidence secrets first.
	s = rePEM.ReplaceAllString(s, "[private-key]")
	s = reURLCreds.ReplaceAllString(s, "${1}[redacted]${2}")
	s = reBearer.ReplaceAllString(s, "${1} [secret]")
	s = reAssignSecret.ReplaceAllString(s, "${1}${2}[secret]")
	// Payment cards (Luhn-checked) before the generic number rule below.
	s = reCardCandidate.ReplaceAllStringFunc(s, func(m string) string {
		d := onlyDigits(m)
		if len(d) >= 13 && len(d) <= 19 && luhn(d) {
			return "[card]"
		}
		return m
	})
	for _, sc := range scrubbers {
		s = sc.re.ReplaceAllString(s, sc.with)
	}
	return s
}

func onlyDigits(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// luhn reports whether an all-digit string passes the Luhn checksum.
func luhn(num string) bool {
	sum, alt := 0, false
	for i := len(num) - 1; i >= 0; i-- {
		d := int(num[i] - '0')
		if alt {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		alt = !alt
	}
	return sum%10 == 0
}
