package priority

import (
	"os"
	"strings"
	"testing"
)

// TestRankingBuildsShareOneDetachedBuilder is a source invariant, not a
// behaviour test: the detached, bounded, Close-cancellable, panic-safe and
// admission-checked ranking build has one owner, buildDetached. Two ranking
// paths rebuild the same snapshot (the cold build that fills the cache and the
// refresh that replaces an expired one), and a second copy of this block is
// how one of them keeps a context after its response, loses the Close cancel,
// or installs a snapshot the resource guard refused. The callers keep only
// what genuinely differs: which snapshot may replace which.
//
// The context and panic mechanics themselves are safe.Detached's, so this
// package must delegate to it once and spell none of them again.
func TestRankingBuildsShareOneDetachedBuilder(t *testing.T) {
	src, err := os.ReadFile("priority.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, shape := range []struct {
		code string
		want int
		why  string
	}{
		{"safe.Detached(", 1, "running the one detached, bounded, Close-cancelled, panic-safe build"},
		{"safe.PanicError", 1, "turning a panic into the caller's error"},
		{"s.admit(buildCtx)", 1, "giving the resource guard the last word inside the bound"},
		{"context.WithoutCancel", 0, "detaching the build, which safe.Detached owns"},
		{"context.AfterFunc", 0, "letting Close cancel the build, which safe.Detached owns"},
		{"safe.Value(", 0, "containing a panicking build, which safe.Detached owns"},
	} {
		if n := strings.Count(body, shape.code); n != shape.want {
			t.Errorf("%q appears %d times in priority.go, want exactly %d (%s)",
				shape.code, n, shape.want, shape.why)
		}
	}
}
