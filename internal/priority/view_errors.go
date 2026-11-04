package priority

import (
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/logx"
)

// countError is the one count-bound problem of the ranked views: a page size
// outside [1, MaxCached] is a caller error, and the message names the view.
func countError(view string, n int) error {
	return fmt.Errorf("priority: %s count must be between 1 and %d, got %d", view, MaxCached, n)
}

// panicError is the one panic-to-error conversion of the ranking reads. The
// value's type is kept (never its contents), which is what a log line needs.
func panicError(what string, value any) error {
	return fmt.Errorf("priority: %s panicked (%T)", what, value)
}

// logRefreshPaused is the one record of a refresh stopped by the resource
// guard; the served ranking stays the cached one either way.
func logRefreshPaused(err error) {
	logx.For("priority").Info("priority ranking refresh paused by the resource guard; serving the cached ranking", "err", err)
}
