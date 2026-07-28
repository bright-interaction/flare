package api

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestEveryIssueTextSurfaceIsLabelled stops the labelling from going stale.
//
// Issue titles, culprits and ai_triage are built from telemetry, and telemetry
// is written by whoever can reach the monitored application: a DSN public key
// ships in a browser bundle. The MCP consumer is an agent that holds write tools
// and usually a shell, so this text is the far end of an injection path from an
// anonymous HTTP POST.
//
// The fix was applied surface by surface and drifted immediately: triage_issue
// got a trust label first, then list_issues and get_issue, while overview's
// top_issues shipped unlabelled. That is arguably worse than labelling nothing,
// because an agent that sees one labelled field infers the unlabelled ones are
// first-party.
//
// So the property is checked across the file rather than per call site: any MCP
// tool result carrying issue text must also carry a trust marker.
func TestEveryIssueTextSurfaceIsLabelled(t *testing.T) {
	src, err := os.ReadFile("mcp.go")
	if err != nil {
		t.Fatalf("read mcp.go: %v", err)
	}
	body := string(src)

	// Each returned map literal that hands back issue-derived text.
	returns := regexp.MustCompile(`(?s)return map\[string\]any\{.*?\}, nil`).FindAllString(body, -1)
	if len(returns) == 0 {
		t.Fatal("no tool result literals found in mcp.go; this guard has lost its target")
	}

	// Field KEYS whose content originates in attacker-written telemetry. Keys,
	// not bare words: a first attempt used the substring "issues" and flagged the
	// initialize response, whose instructions prose merely mentions list_issues.
	// A guard that cries wolf on documentation gets muted, so it has to match the
	// thing it means.
	tainted := []string{`"top_issues":`, `"issues":`, `"issue":`, `"triage":`, `"ai_triage":`}

	var unlabelled []string
	for _, r := range returns {
		carries := false
		for _, f := range tainted {
			if strings.Contains(r, f) {
				carries = true
				break
			}
		}
		if !carries {
			continue
		}
		if !strings.Contains(r, `"trust"`) {
			head := r
			if i := strings.Index(head, "\n"); i > 0 {
				head = head[:i]
			}
			unlabelled = append(unlabelled, head)
		}
	}

	if len(unlabelled) > 0 {
		t.Errorf("%d MCP tool result(s) return issue-derived text with no \"trust\" marker:\n  %s\n\n"+
			"That text comes from telemetry any third party can write, and the caller is an agent "+
			"holding write tools. Labelling some surfaces and not others teaches the agent that the "+
			"unlabelled ones are first-party, which is worse than labelling none.",
			len(unlabelled), strings.Join(unlabelled, "\n  "))
	}
}
