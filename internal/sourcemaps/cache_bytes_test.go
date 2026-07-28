package sourcemaps

import (
	"fmt"
	"strings"
	"testing"
)

// TestCacheIsBoundedByBytesNotJustCount pins the memory ceiling.
//
// The cache used to bound only the ENTRY COUNT at 64. A parsed consumer holds
// the whole decoded map, and an upload may be 30 MiB (maxArtifactBody), so 64
// entries permits ~2.5 GiB. Measured before this fix: 64 synthetic maps at the
// upload ceiling left 2,479 MiB retained, permanently, in the process every
// tenant shares.
//
// That is the same mistake the query-side fix had called out one layer down
// ("a row count does not bound memory"), repeated here because the two limits
// live in different files and nothing connected them.
//
// The test drives real source-map content through the real entry point rather
// than asserting on the constants, so it fails if the accounting is wrong even
// when the constants look right.
func TestCacheIsBoundedByBytesNotJustCount(t *testing.T) {
	r := NewResolver()

	// Each map must be larger than maxCachedBytes/maxCachedConsumers, or the
	// COUNT ceiling alone keeps the total under the byte ceiling and the test
	// cannot tell the two bounds apart. A first attempt used 1 MiB maps and
	// passed against the count-only code it was written to reject: 64 x 1 MiB is
	// 64 MiB, comfortably inside a 256 MiB ceiling.
	//
	// 8 MiB is realistic (uploads may be 30 MiB) and makes 64 entries 512 MiB,
	// which is double the ceiling.
	const each = 8 << 20
	big := func(i int) string {
		return fmt.Sprintf(
			`{"version":3,"sources":["a%d.js"],"names":[],"mappings":"AAAA","sourcesContent":["%s"]}`,
			i, strings.Repeat("x", each))
	}

	// Enough to pass the byte ceiling, and enough that a count-only bound would
	// blow through it.
	n := (maxCachedBytes / each) + 8
	for i := 0; i < n; i++ {
		r.consumer(big(i))
	}

	r.mu.Lock()
	bytes, entries := r.bytes, len(r.cache)
	r.mu.Unlock()

	if bytes > maxCachedBytes {
		t.Errorf("cache retains %d bytes, ceiling %d: the byte bound is not being enforced",
			bytes, maxCachedBytes)
	}
	if entries > maxCachedConsumers {
		t.Errorf("cache holds %d entries, ceiling %d", entries, maxCachedConsumers)
	}
	// The accounting must match reality, or the ceiling is guarding a number
	// that has drifted from the map it describes.
	var sum int
	r.mu.Lock()
	for k := range r.cache {
		sum += r.sizes[k]
	}
	got := r.bytes
	r.mu.Unlock()
	if sum != got {
		t.Errorf("byte accounting drifted: sizes total %d, tracked %d", sum, got)
	}
	t.Logf("after %d x ~8MiB maps: %d entries, %d bytes retained (ceiling %d)",
		n, entries, bytes, maxCachedBytes)
}

// TestCacheStillCachesWithinTheCeiling keeps the fix honest: a bound that
// evicts constantly turns every symbolication into a re-parse and quietly
// removes the cache's reason to exist.
func TestCacheStillCachesWithinTheCeiling(t *testing.T) {
	r := NewResolver()
	const m = `{"version":3,"sources":["a.js"],"names":[],"mappings":"AAAA","sourcesContent":["let a=1"]}`

	first := r.consumer(m)
	second := r.consumer(m)
	if first == nil || second == nil {
		t.Fatal("valid source map failed to parse")
	}
	if first != second {
		t.Error("the same content re-parsed instead of hitting the cache")
	}
}
