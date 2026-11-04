package qa

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/search"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// TestAnswerCacheInvalidatesOnRouteGeneration: a layer-1 re-score rewrites
// the certified route answers an answer cites, without touching mail or
// labels. The mail generation does not move, so a cache keyed on it alone
// would serve an answer built on the replaced route for the whole TTL.
func TestAnswerCacheInvalidatesOnRouteGeneration(t *testing.T) {
	w := newWorld(t, false)
	w.search.result = search.Result{Hits: []search.Hit{hit(1)}}
	w.store.summaries[1] = store.Summary{RowID: 1, Subject: "s", Body: "body"}
	w.gen.content = `{"route":"answer","answer":"Answer [1].","used":[1]}`
	question := "What happened with the budget?"

	ask := func() {
		t.Helper()
		if _, err := w.svc.Ask(t.Context(), question, "en"); err != nil {
			t.Fatal(err)
		}
	}

	ask()
	ask()
	if w.gen.calls != 1 {
		t.Fatalf("the same question called the generator %d times, want 1", w.gen.calls)
	}
	w.store.routeGen++
	ask()
	if w.gen.calls != 2 {
		t.Fatalf("a route commit reused the old answer: %d generator calls", w.gen.calls)
	}
	ask()
	if w.gen.calls != 2 {
		t.Fatalf("the post-route-commit answer was not cached: %d calls", w.gen.calls)
	}
}
