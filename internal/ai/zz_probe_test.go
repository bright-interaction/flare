package ai

import (
	"encoding/json"
	"testing"
)

func TestProbeScrubJSONValidity(t *testing.T) {
	cases := []string{
		`{"http.request_id":"r1","event_time_ms":1721899200000}`,
		`{"start_time_unix_nano":1721899200000000000}`,
		`{"api_key_id":9876543210}`,
		`{"token":"abc\"def"}`,
		`{"nested":{"arr":[1721899200000,"user@x.com",{"password":"hunter2hunter"}]}}`,
		`not json at all 1721899200000`,
		`{"card":"4111 1111 1111 1111"}`,
		`{"big":123456789012345678901234567890}`,
	}
	for _, c := range cases {
		out := ScrubJSON([]byte(c))
		ok := json.Valid(out)
		t.Logf("IN : %s\nOUT: %s  valid=%v", c, string(out), ok)
		if !ok {
			t.Errorf("INVALID JSON produced for %s -> %s", c, string(out))
		}
	}
}
