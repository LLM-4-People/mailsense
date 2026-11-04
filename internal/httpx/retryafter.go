package httpx

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// retryAfter reads the server's requested delay: retry-after-ms
// (milliseconds, may be fractional) wins over Retry-After (seconds or an
// HTTP date). Negative, non-finite or unparseable values are ignored.
func retryAfter(h http.Header, now time.Time) (time.Duration, bool) {
	if v := strings.TrimSpace(h.Get("retry-after-ms")); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil && ms >= 0 && !math.IsInf(ms, 0) && ms < float64(math.MaxInt64/int64(time.Millisecond)) {
			return time.Duration(ms * float64(time.Millisecond)), true
		}
	}
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs < 0 || secs > math.MaxInt64/int64(time.Second) {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	if at, err := http.ParseTime(v); err == nil {
		return max(at.Sub(now), 0), true
	}
	return 0, false
}
