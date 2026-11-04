//go:build fixture && !unix

package evidence

// cpuMillis is unavailable on this platform: the fixture A/B records -1
// instead of inventing a CPU number.
func cpuMillis() int64 { return -1 }
