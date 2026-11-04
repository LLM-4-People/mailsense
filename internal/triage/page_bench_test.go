package triage

import (
	"encoding/json"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// BenchmarkBuildPage measures one routed page build over the test fixture
// with a cold triage core: the cache is dropped before every build so the
// benchmark sees what a rebuild after a generation change pays. The page
// must answer from the route rows and message rows only; before the
// page-only session it built (and discarded) the causal core here.
func BenchmarkBuildPage(b *testing.B) {
	w := newWorld(b, nil)
	p, err := taxonomy.Load(w.cfg)
	if err != nil {
		b.Fatal(err)
	}
	const messages = 120
	rows := make([]store.RouteAnswer, 0, messages)
	for i := range messages {
		id := w.mail("a@client.com", "A", "Budget", 0, 0)
		body, err := json.Marshal(routeAnswerSet(p, i))
		if err != nil {
			b.Fatal(err)
		}
		rows = append(rows, store.RouteAnswer{MessageID: id, Revision: w.revision(b, id), Answers: string(body)})
	}
	commitRoutes(b, w, rows)
	w.useRoute(b, w.st)
	tr := w.tr
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		tr.cacheMu.Lock()
		tr.cache = nil
		tr.cacheMu.Unlock()
		page, err := tr.buildPage(b.Context(), testRouteKey, TriagePageDefault, 0)
		if err != nil {
			b.Fatal(err)
		}
		if len(page.Results) != TriagePageDefault {
			b.Fatalf("page has %d rows, want %d", len(page.Results), TriagePageDefault)
		}
	}
}
