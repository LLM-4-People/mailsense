package outlook

import (
	"math"
	"time"
)

// idleSince is the idle time between two 32-bit millisecond tick counts
// (GetTickCount, GetLastInputInfo): unsigned arithmetic survives one wrap,
// and a difference above 2^31 ms — a last input "after" now — reads as 0.
func idleSince(now, last uint32) time.Duration {
	d := now - last
	if d > math.MaxInt32 {
		return 0
	}
	return time.Duration(d) * time.Millisecond
}
