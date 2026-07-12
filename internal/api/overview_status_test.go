package api

import "testing"

func TestProjectHealth(t *testing.T) {
	buckets := func(vals ...int64) []int64 {
		b := make([]int64, overviewBuckets)
		copy(b[overviewBuckets-len(vals):], vals) // right-align: last val = current hour
		return b
	}
	sum := func(b []int64) int64 {
		var t int64
		for _, v := range b {
			t += v
		}
		return t
	}
	spikeVol := append(make([]int64, overviewBuckets-1), 30)[:overviewBuckets]
	cases := []struct {
		name  string
		alive bool
		vol   []int64
		want  string
	}{
		// Liveness is the headline: no pulse wins over any error state.
		{"no pulse, no errors", false, buckets(), "silent"},
		{"no pulse but recent errors still silent", false, buckets(2, 2, 2, 2), "silent"},
		{"no pulse even mid-spike still silent", false, spikeVol, "silent"},
		// Alive services classify by error activity.
		{"alive and clean is healthy", true, buckets(), "healthy"},
		{"alive with steady errors is alert", true, buckets(2, 2, 2, 2), "alert"},
		{"alive with a current-hour surge is a spike", true, spikeVol, "spike"},
		{"alive small current hour is not a spike", true, buckets(1, 1, 1, 4), "alert"},
	}
	for _, c := range cases {
		if got := projectHealth(c.alive, c.vol, sum(c.vol)); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestHealthRankOrder(t *testing.T) {
	// spike is most urgent, silent is last; the flat list relies on this order.
	order := []string{"spike", "alert", "healthy", "silent"}
	for i := 1; i < len(order); i++ {
		if healthRank(order[i-1]) >= healthRank(order[i]) {
			t.Errorf("expected %q to rank before %q", order[i-1], order[i])
		}
	}
	if healthRank("nonsense") <= healthRank("silent") {
		t.Errorf("unknown status should rank after all known ones")
	}
}
