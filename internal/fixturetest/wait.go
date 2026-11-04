package fixturetest

import (
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/fixturetest/fixturewait"
)

// WaitUntil polls cond until it holds or timeout expires, failing the test
// with what on timeout. The implementation lives in fixturewait so packages
// that fixturetest already depends on (through syncer) can use it too.
func WaitUntil(t testing.TB, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	fixturewait.WaitUntil(t, timeout, what, cond)
}
