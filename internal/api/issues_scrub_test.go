package api

import (
	"strings"
	"testing"

	"github.com/bright-interaction/flare/internal/telemetry"
)

func TestToIssueResponseScrubsSensitive(t *testing.T) {
	// A flagged issue must not re-serve the leaked value in its title/culprit.
	i := telemetry.Issue{
		ID:        "x",
		Title:     "Error: token gho_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123 rejected",
		Culprit:   "charge 4111 1111 1111 1111",
		Sensitive: "secret,card",
	}
	r := toIssueResponse(i)
	if strings.Contains(r.Title, "gho_ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		t.Errorf("secret survived in title: %q", r.Title)
	}
	if strings.Contains(r.Culprit, "4111 1111 1111 1111") {
		t.Errorf("card survived in culprit: %q", r.Culprit)
	}

	// A non-sensitive issue is passed through untouched.
	n := toIssueResponse(telemetry.Issue{ID: "y", Title: "NullPointer at handler()", Sensitive: ""})
	if n.Title != "NullPointer at handler()" {
		t.Errorf("non-sensitive title should be unchanged: %q", n.Title)
	}
}
