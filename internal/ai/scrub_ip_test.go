package ai

import "testing"

// IPv6 addresses used to egress raw through every MCP tool while the
// equivalent IPv4 address was redacted. The MAC and clock-time cases guard
// against the compressed-form rule over-matching.
func TestScrubRedactsIPv6WithoutEatingMACsOrTimes(t *testing.T) {
	cases := map[string]string{
		"client 2001:0db8:85a3:0000:0000:8a2e:0370:7334 hit": "[ip]",
		"from fe80::1 ok":              "[ip]",
		"mapped ::ffff:192.0.2.1 seen": "[ip]",
		"ipv4 192.168.1.24 seen":       "[ip]",
		"mac 00:1b:44:11:3a:b7 nic":    "[mac]",
		"at 10:30:45 today":            "",
		"see http://example.com/x":     "",
	}
	for in, want := range cases {
		got := Scrub(in)
		t.Logf("%-55q -> %q", in, got)
		if want != "" && !containsSub(got, want) {
			t.Errorf("Scrub(%q) = %q, expected it to contain %q", in, got, want)
		}
		if want == "" && got != in {
			t.Errorf("Scrub(%q) = %q, expected unchanged", in, got)
		}
	}
}

func containsSub(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
