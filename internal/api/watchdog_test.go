package api

import "testing"

func TestAnomalyReason(t *testing.T) {
	// window 60m -> 24 windows/day. baseline excludes the recent window.
	cases := []struct {
		name           string
		recent, day24h int64
		window, mult   int32
		wantFire       bool
	}{
		{"below floor never fires", 4, 4, 60, 3, false},
		{"steady load, no spike", 10, 250, 60, 3, false}, // baseline ~10.4, 10 < 3x
		{"3x baseline fires", 40, 250, 60, 3, true},      // baseline ~9.1, 40 >= 27
		{"quiet baseline, small burst below floor", 3, 3, 60, 3, false},
		{"quiet baseline, burst over floor fires", 12, 12, 60, 3, true}, // baseline ~0 -> floor governs
		{"coarse window rejected", 20, 20, 1440, 3, false},              // 1 window/day, no baseline
		{"windowMin zero is safe, no panic", 20, 200, 0, 3, false},
		{"recent over day is clamped, floor still fires", 30, 5, 60, 3, true},
	}
	for _, c := range cases {
		got := anomalyReason(c.recent, c.day24h, c.window, c.mult) != ""
		if got != c.wantFire {
			t.Errorf("%s: fired=%v want %v", c.name, got, c.wantFire)
		}
	}
}

func TestSilenceReason(t *testing.T) {
	cases := []struct {
		name           string
		recent, day24h int64
		minActive      int32
		wantFire       bool
	}{
		{"still active, no silence", 3, 500, 20, false},
		{"silent and normally active fires", 0, 500, 20, true},
		{"silent but normally quiet, no alert", 0, 5, 20, false},
		{"silent, default minActive 20, active", 0, 25, 0, true},
	}
	for _, c := range cases {
		got := silenceReason(c.recent, c.day24h, c.minActive) != ""
		if got != c.wantFire {
			t.Errorf("%s: fired=%v want %v", c.name, got, c.wantFire)
		}
	}
}
