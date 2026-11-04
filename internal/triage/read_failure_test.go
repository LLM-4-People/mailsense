package triage

import (
	"encoding/json"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/learning/learningtest"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// TestRoutedReadKeepsFailure: a message whose last attempt failed may have no
// route row at all. The routed read and the routed page must keep that
// failure, so their coverage and per-row chip agree with the summary instead
// of reporting zero failed.
func TestRoutedReadKeepsFailure(t *testing.T) {
	w := newWorld(t, nil)
	p, err := taxonomy.Load(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := w.mail("a@client.com", "A", "Budget", 0, 0)
	body, err := json.Marshal(routeAnswerSet(p, 0))
	if err != nil {
		t.Fatal(err)
	}
	commitRoutes(t, w, []store.RouteAnswer{{MessageID: id, Revision: w.revision(t, id), Answers: string(body)}})
	w.useRoute(t, w.st)

	sum, err := w.tr.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.st.SaveFailure(t.Context(), sum.Key, store.Failure{MessageID: id, Error: "provider down"}); err != nil {
		t.Fatal(err)
	}

	// A failure write moves no generation, so a fresh triager is the
	// production read that sees it.
	tr, err := New(w.cfg, w.st, w.bus, w.src, learningtest.Open{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tr.UseRoute(w.st, w.routeKeyer(t))

	view, err := tr.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if view.Coverage.Failed != 1 || view.Coverage.Eligible != 1 {
		t.Fatalf("routed read coverage %+v, want one failure", view.Coverage)
	}
	if got := view.Results[id].Failure; got != "provider down" {
		t.Fatalf("routed read lost the failure: %q", got)
	}

	page, err := tr.Page(t.Context(), PageQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := page.Results[id].Failure; got != "provider down" {
		t.Fatalf("routed page lost the failure: %q", got)
	}
}

// TestRoutedReadAndPageAgreeOnFailureAndCoverage pins the one route-answer
// load behind both routed surfaces: for the same key, ids and stored failure,
// Read and Page report the same failure text and the same coverage counts.
func TestRoutedReadAndPageAgreeOnFailureAndCoverage(t *testing.T) {
	w := newWorld(t, nil)
	p, err := taxonomy.Load(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := w.mail("a@client.com", "A", "Budget", 0, 0)
	body, err := json.Marshal(routeAnswerSet(p, 0))
	if err != nil {
		t.Fatal(err)
	}
	commitRoutes(t, w, []store.RouteAnswer{{MessageID: id, Revision: w.revision(t, id), Answers: string(body)}})
	w.useRoute(t, w.st)

	sum, err := w.tr.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.st.SaveFailure(t.Context(), sum.Key, store.Failure{MessageID: id, Error: "provider down"}); err != nil {
		t.Fatal(err)
	}

	// A failure write moves no generation, so a fresh triager is the
	// production read that sees it.
	tr, err := New(w.cfg, w.st, w.bus, w.src, learningtest.Open{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tr.UseRoute(w.st, w.routeKeyer(t))

	view, err := tr.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	page, err := tr.Page(t.Context(), PageQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}

	res, ok := view.Results[id]
	if !ok {
		t.Fatalf("the read dropped the message: %+v", view.Coverage)
	}
	row, ok := page.Results[id]
	if !ok {
		t.Fatalf("the page dropped the message: %+v", page.Results)
	}
	if res.Failure != row.Failure || res.Failure != "provider down" {
		t.Fatalf("failure text differs: read %q page %q", res.Failure, row.Failure)
	}

	// The page's judged population is the read's decided coverage, and its
	// failed rows are the read's Failed count.
	judged := view.Coverage.Fresh + view.Coverage.Partial + view.Coverage.Stale
	failed := 0
	for _, r := range page.Results {
		if r.Failure != "" {
			failed++
		}
	}
	if page.Total != judged || len(page.Results) != judged {
		t.Fatalf("coverage population differs: read %+v page total %d rows %d", view.Coverage, page.Total, len(page.Results))
	}
	if view.Coverage.Failed != failed {
		t.Fatalf("failed count differs: read %d page %d", view.Coverage.Failed, failed)
	}

	// The per-row coverage facts are the same projection on both surfaces.
	if row.Missing != len(res.Missing) || row.Stale != res.Stale || row.DecidedAt != res.DecidedAt ||
		row.Escalate != res.Escalate || row.Preview != res.Preview {
		t.Fatalf("row coverage differs: read %+v page %+v", res, row)
	}
}
