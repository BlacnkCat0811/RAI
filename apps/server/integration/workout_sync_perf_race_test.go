//go:build integration && race

package integration

// perfRaceEnabled reports whether this test binary was compiled with the race
// detector. R0 requires the optimized build, so the syncperf entry point refuses
// to measure a race-instrumented binary.
const perfRaceEnabled = true
