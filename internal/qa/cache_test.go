package qa

import (
	"testing"
	"time"
)

// A repeated freeform question is answered once. The same question on another
// owner-local day, in another zone, or after the replay window is a miss:
// the plan is date-relative and the prompt embeds today.
func TestAnswerCacheExpiresWithTheOwnerDay(t *testing.T) {
	w := newWorld(t, false)
	clock := w.svc.clock.(*fixedClock)
	planFallback(w)
	ask := func() {
		t.Helper()
		if _, err := w.svc.Ask(t.Context(), "How many invoices arrived this month?", "en"); err != nil {
			t.Fatal(err)
		}
	}
	ask()
	if w.gen.calls != 1 {
		t.Fatalf("first ask called the provider %d times", w.gen.calls)
	}
	ask()
	if w.gen.calls != 1 {
		t.Fatalf("a repeated ask re-called the provider: %d calls", w.gen.calls)
	}
	clock.now = clock.now.Add(11 * time.Minute)
	ask()
	if w.gen.calls != 2 {
		t.Fatalf("an expired answer was replayed: %d calls", w.gen.calls)
	}
	clock.now = clock.now.Add(24 * time.Hour)
	ask()
	if w.gen.calls != 3 {
		t.Fatalf("an answer from another day was replayed: %d calls", w.gen.calls)
	}
}
