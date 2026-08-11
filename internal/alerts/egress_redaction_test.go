package alerts

import (
	"encoding/json"
	"strings"
	"testing"
)

// M7. The fallback `text` is what Slack renders as the push and desktop
// notification, so it is the FIRST thing the on-call engineer sees, and it was
// the one string in this payload that skipped slackEsc. slack_test.go asserted
// escaping on blocks[0].text only, which is exactly why this shipped.
func TestSlackFallbackTextIsEscaped(t *testing.T) {
	n := Notification{
		ProjectName: "api",
		Reason:      "New issue",
		Title:       `boom <!channel> <https://evil.example|Click here to reset your Flare password>`,
		Level:       "error",
		URL:         "https://flare.example/issues/1",
	}
	var msg struct {
		Text   string           `json:"text"`
		Blocks []map[string]any `json:"blocks"`
	}
	if err := json.Unmarshal(slackPayload(n), &msg); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if strings.Contains(msg.Text, "<!channel>") {
		t.Fatalf("the push notification pages the whole channel: %q", msg.Text)
	}
	if strings.Contains(msg.Text, "<https://evil.example|") {
		t.Fatalf("the push notification carries an attacker-authored link: %q", msg.Text)
	}
	if !strings.Contains(msg.Text, "&lt;!channel&gt;") {
		t.Fatalf("fallback text was not escaped at all: %q", msg.Text)
	}
}
