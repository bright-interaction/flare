package alerts

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSlackPayload(t *testing.T) {
	n := Notification{
		ProjectName: "payments-api",
		Title:       "Error: card <declined>",
		Level:       "error",
		EventCount:  12,
		Reason:      "Regression",
		URL:         "https://flare.example/issues/abc",
	}
	var msg struct {
		Text   string           `json:"text"`
		Blocks []map[string]any `json:"blocks"`
	}
	if err := json.Unmarshal(slackPayload(n), &msg); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if !strings.Contains(msg.Text, "Regression") {
		t.Errorf("fallback text missing reason: %q", msg.Text)
	}
	if len(msg.Blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(msg.Blocks))
	}
	section, _ := msg.Blocks[0]["text"].(map[string]any)
	text, _ := section["text"].(string)
	if !strings.Contains(text, "payments-api") || !strings.Contains(text, n.URL) {
		t.Errorf("section missing project/url: %q", text)
	}
	// angle brackets in the title must be escaped so they don't break mrkdwn links.
	if strings.Contains(text, "<declined>") {
		t.Errorf("title angle brackets not escaped: %q", text)
	}
}
