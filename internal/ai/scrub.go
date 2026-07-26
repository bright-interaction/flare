package ai

import (
	"bytes"
	"encoding/json"
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

// ScrubJSON scrubs a JSON document by walking it and applying Scrub to string
// leaves only, then re-marshalling.
//
// Never run Scrub over serialized JSON directly: the number rules
// (\b\d{13,19}\b -> [number] and the Luhn card rule) rewrite UNQUOTED numeric
// values, so {"duration_ns":1712345678901234567} became
// {"duration_ns":[number]}, which is not valid JSON. Callers wrap the result in
// json.RawMessage, and encoding/json then fails on the whole response; the MCP
// layer's marshal fallback rendered the []byte as a decimal byte dump.
// Nanosecond timestamps are 19 digits, so this fired on ordinary OTLP traffic.
//
// The return value is ALWAYS valid JSON, including for input that was not JSON
// to begin with: callers embed it as a json.RawMessage, so returning anything
// else would reintroduce the same failure by another route.
func ScrubJSON(raw []byte) []byte {
	if len(raw) == 0 {
		return raw
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	// Numbers stay as their literal text. Decoding into float64 silently rounds
	// every integer above 2^53, so a 19-digit id or nanosecond timestamp would
	// come back to the operator as 1.7123456789012346e+18.
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		// Not JSON. Scrub it as text and return it as a JSON *string*, which is
		// still valid JSON and safe to embed.
		if out, merr := marshalJSON(Scrub(string(raw))); merr == nil {
			return out
		}
		return []byte(`"[unserializable]"`)
	}
	out, err := marshalJSON(scrubValue(v))
	if err != nil {
		return []byte(`{"_scrub_error":"redacted"}`)
	}
	return out
}

// scrubValue rewrites string leaves. Object KEYS are deliberately left alone:
// they are structure, not payload, and scrubbing them can map two distinct keys
// onto the same string, which silently drops one of the values.
func scrubValue(v any) any {
	switch t := v.(type) {
	case string:
		return Scrub(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = scrubValue(val)
		}
		return out
	case []any:
		for i := range t {
			t[i] = scrubValue(t[i])
		}
		return t
	default:
		return v
	}
}

// marshalJSON encodes without HTML-escaping so <, > and & inside a stack trace
// or a log body are not mangled into \u003c on their way to the model.
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
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
