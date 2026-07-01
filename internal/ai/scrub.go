package ai

import "regexp"

// Scrub removes common PII/secret shapes from text before it is sent to the
// model - the sovereign guarantee: raw personal data and credentials never
// leave the tenant's boundary. Code structure (file names, functions, line
// numbers) is preserved so triage stays useful.
var scrubbers = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`eyJ[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}`), "[jwt]"},
	{regexp.MustCompile(`\b(?:sk|pk|ghp|ghs|xox[baprs]|AKIA)[-_A-Za-z0-9]{16,}\b`), "[secret]"},
	{regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`), "[email]"},
	{regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`), "[ip]"},
	{regexp.MustCompile(`\b(?:[0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}\b`), "[mac]"},
	{regexp.MustCompile(`\b\d{13,19}\b`), "[number]"},
	{regexp.MustCompile(`\b[A-Fa-f0-9]{40,}\b`), "[hash]"},
}

// Scrub returns text with PII/secret shapes replaced by placeholders.
func Scrub(s string) string {
	for _, sc := range scrubbers {
		s = sc.re.ReplaceAllString(s, sc.with)
	}
	return s
}
