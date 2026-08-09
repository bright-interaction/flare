//go:build !race

package ratelimit

// raceDetectorEnabled lets the complexity guard in evict_bench_test.go pick a
// threshold calibrated for the build it is actually running in. See the comment
// on maxGrowthRatio for why a complexity assertion cannot use one number for
// both builds.
const raceDetectorEnabled = false
