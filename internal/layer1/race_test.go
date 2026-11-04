//go:build race

package layer1

// raceDetector marks a race-instrumented build: heap accounting includes the
// detector's own allocations, so the retained-byte bounds are not comparable.
const raceDetector = true
