package api

import "testing"

func TestProjectStatus(t *testing.T) {
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
	cases := []struct {
		name string
		vol  []int64
		want string
	}{
		{"no events", buckets(), "quiet"},
		{"steady low", buckets(2, 2, 2, 2), "ok"},
		{"current hour spikes over 3x avg", append(make([]int64, overviewBuckets-1), 30)[:overviewBuckets], "spike"},
		{"small current hour not a spike", buckets(1, 1, 1, 4), "ok"},
	}
	for _, c := range cases {
		if got := projectStatus(c.vol, sum(c.vol)); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
