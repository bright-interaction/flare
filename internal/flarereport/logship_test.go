package flarereport

import "testing"

// TestParseDSNForLogs locks the DSN -> native-logs endpoint mapping the log
// shipper routes on; a wrong endpoint or key means logs silently never land.
func TestParseDSNForLogs(t *testing.T) {
	endpoint, key, ok := parseDSNForLogs("https://pubkey123@flare.example.com/42")
	if !ok {
		t.Fatal("valid DSN should parse")
	}
	if endpoint != "https://flare.example.com/api/42/logs" {
		t.Errorf("endpoint = %q", endpoint)
	}
	if key != "pubkey123" {
		t.Errorf("key = %q", key)
	}

	for _, bad := range []string{"", "not a url", "https://flare.example.com/42", "https://pubkey@flare.example.com/"} {
		if _, _, ok := parseDSNForLogs(bad); ok {
			t.Errorf("expected %q to be rejected", bad)
		}
	}
}
