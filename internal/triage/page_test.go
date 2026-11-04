package triage

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// routeAnswerSet answers every profile question once (choice: the first
// option, noul: yes, score: level 1).
func routeAnswerSet(p *taxonomy.Profile, variant int) map[string]route.Answer {
	out := map[string]route.Answer{}
	for i := range p.Questions {
		q := &p.Questions[i]
		a := engine.Answer{Type: q.Type}
		switch q.Type {
		case engine.Choice:
			choice := q.Options[variant%len(q.Options)].Name
			c := 0.9
			a.Choice, a.Confidence = choice, &c
			a.Probabilities = map[string]float64{choice: 0.9}
		case engine.Noul:
			a.Value = 0.9
		case engine.Score:
			a.Value = float64(1 + variant%q.Top())
		}
		out[q.Name] = route.Answer{Answer: a}
	}
	return out
}

// commitRoutes writes one route row per answer set in one layer-1 commit.
func commitRoutes(t testing.TB, w *world, rows []store.RouteAnswer) {
	t.Helper()
	p, err := taxonomy.Load(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	run, err := w.st.BeginLayer1Run(t.Context(), "perf-page")
	if err != nil {
		t.Fatal(err)
	}
	err = w.st.CommitLayer1(t.Context(), store.Layer1Commit{RunID: run, Report: func() (string, error) { return "{}", nil },
		Key:       store.TriageKey{Fingerprint: p.Fingerprint, Decider: "layer1"},
		RouteKey:  testRouteKey,
		Decisions: func(fn func(store.Decision) error) error { return nil },
		Routes: func(fn func(store.RouteAnswer) error) error {
			for _, r := range rows {
				if err := fn(r); err != nil {
					return err
				}
			}
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTriagePageIsBoundedAndDeterministic(t *testing.T) {
	w := newWorld(t, nil)
	p, err := taxonomy.Load(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	var rows []store.RouteAnswer
	for i := range 5 {
		id := w.mail("a@client.com", "A", "Budget", 0, 0)
		ids = append(ids, id)
		body, err := json.Marshal(routeAnswerSet(p, i))
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, store.RouteAnswer{MessageID: id, Revision: w.revision(t, id), Answers: string(body)})
	}
	commitRoutes(t, w, rows)
	w.useRoute(t, w.st)
	wantOrder := []int64{ids[4], ids[3], ids[2], ids[1], ids[0]}
	first, err := w.tr.Page(t.Context(), PageQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 5 || first.Limit != 2 || first.Offset != 0 || !first.Routed || len(first.Results) != 2 ||
		first.Results[wantOrder[0]].MessageID != wantOrder[0] || first.Results[wantOrder[1]].MessageID != wantOrder[1] {
		t.Fatalf("first page %+v", first)
	}
	second, err := w.tr.Page(t.Context(), PageQuery{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Results) != 2 || second.Results[wantOrder[2]].MessageID != wantOrder[2] || second.Results[wantOrder[3]].MessageID != wantOrder[3] {
		t.Fatalf("second page %+v", second)
	}
	last, err := w.tr.Page(t.Context(), PageQuery{Limit: 2, Offset: 4})
	if err != nil || last.Total != 5 || len(last.Results) != 1 || last.Results[wantOrder[4]].MessageID != wantOrder[4] {
		t.Fatalf("last page %+v %v", last, err)
	}
	empty, err := w.tr.Page(t.Context(), PageQuery{Limit: 2, Offset: 5})
	if err != nil || empty.Total != 5 || len(empty.Results) != 0 {
		t.Fatalf("past the end %+v %v", empty, err)
	}
	defaults, err := w.tr.Page(t.Context(), PageQuery{})
	if err != nil || defaults.Limit != TriagePageDefault || defaults.Total != 5 || len(defaults.Results) != 5 {
		t.Fatalf("defaults %+v %v", defaults, err)
	}
	for _, q := range []PageQuery{{Limit: -1}, {Limit: TriagePageMax + 1}, {Offset: -1}} {
		if _, err := w.tr.Page(t.Context(), q); err == nil {
			t.Errorf("misuse accepted: %+v", q)
		}
	}
}

// assertPageIDsMatchResults is the IDs contract: exactly the shown rows,
// once each, and in the same order as Results' display order.
func assertPageIDsMatchResults(t *testing.T, page *Page) {
	t.Helper()
	if len(page.IDs) != len(page.Results) {
		t.Fatalf("ids carries %d rows, results %d: %v", len(page.IDs), len(page.Results), page.IDs)
	}
	seen := make(map[int64]bool, len(page.IDs))
	for _, id := range page.IDs {
		if _, ok := page.Results[id]; !ok {
			t.Fatalf("ids carries hidden message %d: %v", id, page.IDs)
		}
		if seen[id] {
			t.Fatalf("ids repeats message %d: %v", id, page.IDs)
		}
		seen[id] = true
	}
	for id := range page.Results {
		if !seen[id] {
			t.Fatalf("results carries message %d, which ids omits: %v", id, page.IDs)
		}
	}
}

// TestTriagePageOrdersByInstantNotRowID is the backfill regression at the
// page contract: the newer message is inserted first (lower row id) and the
// older message second (higher row id), yet the page lists the newer first and
// IDs follows that order.
func TestTriagePageOrdersByInstantNotRowID(t *testing.T) {
	w := newWorld(t, nil)
	p, err := taxonomy.Load(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	newer := w.mail("a@client.com", "A", "Newer", 2*time.Hour, 0)
	older := w.mail("b@client.com", "B", "Older", -2*time.Hour, 0)
	if newer >= older {
		t.Fatalf("fixture row ids: newer %d, older %d (want the older message inserted last)", newer, older)
	}
	var rows []store.RouteAnswer
	for i, id := range []int64{newer, older} {
		body, err := json.Marshal(routeAnswerSet(p, i))
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, store.RouteAnswer{MessageID: id, Revision: w.revision(t, id), Answers: string(body)})
	}
	commitRoutes(t, w, rows)
	w.useRoute(t, w.st)

	page, err := w.tr.Page(t.Context(), PageQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Results) != 2 {
		t.Fatalf("page total %d with %d results", page.Total, len(page.Results))
	}
	if want := []int64{newer, older}; !reflect.DeepEqual(page.IDs, want) {
		t.Fatalf("ids = %v, want newest first %v", page.IDs, want)
	}
	assertPageIDsMatchResults(t, page)
	if page.Results[page.IDs[0]].MessageID != newer {
		t.Fatalf("the first row is %d, want the newer message %d", page.Results[page.IDs[0]].MessageID, newer)
	}
}

// TestTriagePageIDsSkipHiddenRowsAndEncodeAsArray proves IDs names only rows
// pageResult actually shows: a route row with no usable answers consumes a
// window slot (and Total) but is absent from Results, so it must not leave a
// dangling id. The wire carries ids as an array and the existing fields
// unchanged.
func TestTriagePageIDsSkipHiddenRowsAndEncodeAsArray(t *testing.T) {
	w := newWorld(t, nil)
	p, err := taxonomy.Load(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	older := w.mail("a@client.com", "A", "Older", -2*time.Hour, 0)
	hidden := w.mail("b@client.com", "B", "Hidden", 0, 0)
	newer := w.mail("c@client.com", "C", "Newer", 2*time.Hour, 0)
	var rows []store.RouteAnswer
	for _, entry := range []struct {
		id      int64
		answers map[string]route.Answer
	}{
		{older, routeAnswerSet(p, 1)},
		{hidden, map[string]route.Answer{}}, // no profile question: pageResult hides it
		{newer, routeAnswerSet(p, 0)},
	} {
		body, err := json.Marshal(entry.answers)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, store.RouteAnswer{MessageID: entry.id, Revision: w.revision(t, entry.id), Answers: string(body)})
	}
	commitRoutes(t, w, rows)
	w.useRoute(t, w.st)

	page, err := w.tr.Page(t.Context(), PageQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || len(page.Results) != 2 {
		t.Fatalf("page total %d with %d results, want 3/2", page.Total, len(page.Results))
	}
	if _, ok := page.Results[hidden]; ok {
		t.Fatalf("the hidden row reached Results: %+v", page.Results[hidden])
	}
	if want := []int64{newer, older}; !reflect.DeepEqual(page.IDs, want) {
		t.Fatalf("ids = %v, want only the shown rows newest first %v", page.IDs, want)
	}
	assertPageIDsMatchResults(t, page)

	body, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		IDs     []int64            `json:"ids"`
		Results map[string]PageRow `json:"results"`
		Routed  bool               `json:"routed"`
		Preview bool               `json:"preview"`
		Total   int                `json:"total"`
		Limit   int                `json:"limit"`
		Offset  int                `json:"offset"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(wire.IDs, []int64{newer, older}) || len(wire.Results) != 2 {
		t.Fatalf("wire ids %v, results %d", wire.IDs, len(wire.Results))
	}
	if !wire.Routed || wire.Preview != page.Preview || wire.Total != page.Total ||
		wire.Limit != page.Limit || wire.Offset != page.Offset {
		t.Fatalf("wire envelope %+v, page %+v", wire, page)
	}
	if wire.Results[fmt.Sprint(newer)].MessageID != newer || wire.Results[fmt.Sprint(older)].MessageID != older {
		t.Fatalf("wire results lost their rows: %+v", wire.Results)
	}
	// "ids" is an array even for an empty page, never null.
	empty, err := w.tr.Page(t.Context(), PageQuery{Limit: TriagePageDefault, Offset: 3})
	if err != nil {
		t.Fatal(err)
	}
	emptyBody, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(emptyBody, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["ids"]) != "[]" {
		t.Fatalf("empty page ids = %s, want []", raw["ids"])
	}
}

// TestTriagePageDeciderIDsFollowItsNaturalOrder covers the test-only path
// without a route: pageDecider orders its own page by descending row id and
// publishes exactly the rows it put in Results.
func TestTriagePageDeciderIDsFollowItsNaturalOrder(t *testing.T) {
	w := newWorld(t, nil)
	p, err := taxonomy.Load(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	ids := []int64{
		w.mail("a@client.com", "A", "One", 0, 0),
		w.mail("b@client.com", "B", "Two", time.Hour, 0),
		w.mail("c@client.com", "C", "Three", 2*time.Hour, 0),
	}
	s, err := w.tr.prepare(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var decisions []store.Decision
	for i, id := range ids {
		answers := map[string]engine.Answer{}
		for name, answer := range routeAnswerSet(p, i) {
			answers[name] = answer.Answer
		}
		body, err := json.Marshal(answers)
		if err != nil {
			t.Fatal(err)
		}
		decisions = append(decisions, store.Decision{MessageID: id, Revision: w.revision(t, id), StateHash: "test", Answers: string(body)})
	}
	if err := w.st.SaveDecisions(t.Context(), s.key, decisions); err != nil {
		t.Fatal(err)
	}
	page, err := w.tr.Page(t.Context(), PageQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{ids[2], ids[1]}; !reflect.DeepEqual(page.IDs, want) {
		t.Fatalf("first decider page ids = %v, want %v", page.IDs, want)
	}
	assertPageIDsMatchResults(t, page)
	second, err := w.tr.Page(t.Context(), PageQuery{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{ids[0]}; !reflect.DeepEqual(second.IDs, want) {
		t.Fatalf("second decider page ids = %v, want %v", second.IDs, want)
	}
	assertPageIDsMatchResults(t, second)
	empty, err := w.tr.Page(t.Context(), PageQuery{Limit: 2, Offset: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.IDs) != 0 || empty.IDs == nil {
		t.Fatalf("past the end ids = %#v, want an empty array", empty.IDs)
	}
}

func TestTriageSummaryCoverageAndKey(t *testing.T) {
	w := newWorld(t, nil)
	p, err := taxonomy.Load(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	fresh := w.mail("a@client.com", "A", "Fresh", 0, 0)
	partial := w.mail("b@client.com", "B", "Partial", 0, 0)
	stale := w.mail("c@client.com", "C", "Stale", 0, 0)
	unjudged := w.mail("d@client.com", "D", "Unjudged", 0, 0)

	partialAnswers := routeAnswerSet(p, 1)
	delete(partialAnswers, p.Questions[0].Name)
	var rows []store.RouteAnswer
	for _, entry := range []struct {
		id   int64
		rev  int64
		body map[string]route.Answer
	}{{fresh, w.revision(t, fresh), routeAnswerSet(p, 0)}, {partial, w.revision(t, partial), partialAnswers}, {stale, w.revision(t, stale) - 1, routeAnswerSet(p, 0)}} {
		body, err := json.Marshal(entry.body)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, store.RouteAnswer{MessageID: entry.id, Revision: entry.rev, Answers: string(body)})
	}
	commitRoutes(t, w, rows)
	w.useRoute(t, w.st)
	sum, err := w.tr.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// The legacy buildSummary produced these exact counts for this fixture
	// (fresh 1, partial 1, stale 1, unjudged 1). The aggregate must keep
	// them: the store test compares both implementations over the same rows.
	if sum.Coverage.Eligible != 4 || sum.Coverage.Fresh != 1 || sum.Coverage.Partial != 1 || sum.Coverage.Stale != 1 ||
		sum.Coverage.Unjudged != 1 || sum.Coverage.Failed != 0 || sum.Total != 3 {
		t.Fatalf("coverage %+v total %d", sum.Coverage, sum.Total)
	}
	if sum.Key.Decider == "" || sum.Key.Fingerprint != p.Fingerprint || !sum.Routed {
		t.Fatalf("key %+v routed %v", sum.Key, sum.Routed)
	}
	// No message content and no per-question distribution in the summary.
	body, err := json.Marshal(sum)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"Fresh", "Partial", "Stale", "Unjudged", "a@client.com", "Budget", "questions"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("summary leaks %q: %s", forbidden, body)
		}
	}
	_ = unjudged
}
