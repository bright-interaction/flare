package api

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/bright-interaction/flare/internal/db/generated"
)

// TestMoreSensitiveRuleRanksAcrossBothAxes pins the ordering used to decide
// which spike reason is reported when several fire at once.
//
// The audit noted the existing tests only vary one axis at a time, which is how
// a comparator ends up looking right and ordering wrong.
func TestMoreSensitiveRuleRanksAcrossBothAxes(t *testing.T) {
	rule := func(threshold int32, window int32) *generated.AlertRule {
		return &generated.AlertRule{Threshold: threshold, WindowMinutes: window}
	}
	cases := []struct {
		name string
		a, b *generated.AlertRule
		want bool
		why  string
	}{
		{"lower threshold wins", rule(10, 5), rule(50, 5), true, "10 events is a tighter trigger than 50"},
		{"higher threshold loses", rule(50, 5), rule(10, 5), false, ""},
		{"equal threshold: longer window wins", rule(10, 1440), rule(10, 5), true,
			"10 in 24h is easier to reach than 10 in 5min, so it is the more sensitive rule"},
		{"equal threshold: shorter window loses", rule(10, 5), rule(10, 1440), false, ""},
		// Both axes differ: threshold decides, which is the case a single-axis
		// test cannot see.
		{"threshold beats window", rule(10, 5), rule(50, 1440), true,
			"threshold is the primary key; a 5min window does not rescue a 50 threshold"},
		{"identical rules are not more sensitive than each other", rule(10, 5), rule(10, 5), false, ""},
	}
	for _, c := range cases {
		if got := moreSensitiveRule(c.a, c.b); got != c.want {
			t.Errorf("%s: moreSensitiveRule(thr=%d/win=%d, thr=%d/win=%d) = %v, want %v. %s",
				c.name, c.a.Threshold, c.a.WindowMinutes, c.b.Threshold, c.b.WindowMinutes, got, c.want, c.why)
		}
	}
}

// TestEverySpikeRuleIsEvaluated is a source assertion, because the behaviour
// lives inside a detached goroutine that takes a DB handle and a live project,
// which no unit test in this package can drive.
//
// The defect: spike rules were collapsed into one per type alongside the boolean
// rules, so only the "most sensitive" ever evaluated. moreSensitiveRule ranks on
// threshold first, so a project with "10 in 5min" and "50 in 1440min" ran only
// the first, and the slow-burn detector never fired. Nothing in the UI said a
// configured rule had been discarded.
func TestEverySpikeRuleIsEvaluated(t *testing.T) {
	src, err := os.ReadFile("ingest_handlers.go")
	if err != nil {
		t.Fatalf("read ingest_handlers.go: %v", err)
	}
	body := string(src)
	i := strings.Index(body, "func (s *Server) evaluateAlerts(")
	if i < 0 {
		t.Fatal("evaluateAlerts not found")
	}
	fn := body[i:]
	if j := regexp.MustCompile(`(?m)^func `).FindStringIndex(fn[1:]); j != nil {
		fn = fn[:j[0]+1]
	}

	// The single-rule lookup is the defect. Anything that pulls one spike rule
	// out of a per-type map evaluates exactly one.
	if regexp.MustCompile(`byType\["spike"\]`).MatchString(fn) {
		t.Error(`evaluateAlerts still selects a single spike rule via byType["spike"]. ` +
			`Every spike rule must be evaluated: they are not interchangeable, and collapsing ` +
			`them silently disables the ones an operator configured.`)
	}
	if !strings.Contains(fn, "range spikes") {
		t.Error("evaluateAlerts does not loop over the spike rules; a project with two spike " +
			"rules would only ever evaluate one")
	}
	// Ordering must stay deterministic, or the reason text for a multi-match
	// event changes run to run and looks like flapping.
	if !strings.Contains(fn, "sort.SliceStable") {
		t.Error("spike rules are not ordered deterministically; the reported reason would vary " +
			"between runs when more than one rule matches")
	}
}
