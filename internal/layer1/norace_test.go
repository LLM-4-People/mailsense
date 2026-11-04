//go:build !race

package layer1

// raceDetector marks a race-instrumented build (see race_test.go).
const raceDetector = false
