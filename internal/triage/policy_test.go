package triage

import (
	"testing"
	"time"
)

// Review of the crash-safety round, #9 (failed first: 2 paid calls): a
// change of DERIVED data — the cleaning rules, the evidence facts, the
// thread views — changed every state it touched, and the plan decided all
// of them again (PAID: ≈1,577 fixture messages after one clean.json edit).
// The lead's policy, at the one choke point (classify): a paid decision
// stays valid for its message until the message's own revision changes
// (or the question set or decider: another key). A stale decision is
// counted with its re-decision cost and decided again only on the user's
// action (Options.Redecide).
func TestDerivedDataNeverRebillsPaidDecisions(t *testing.T) {
	w := newWorld(t, nil)
	row := w.mail("ann@y.com", "c1", "Budget", 0, time.Hour)
	first := w.run(Options{})
	paid := w.dec.count()
	if first.Decided == 0 || paid == 0 {
		t.Fatalf("first run: %+v", first)
	}
	// The derived view changes (evidence facts join the states); no message changes.
	w.tr.UseEvidence(&fakeEvidence{facts: map[int64]string{row: "list_id: no"}, version: "1"})
	if err := w.cfg.Set("triage.facts.evidence", true); err != nil {
		t.Fatal(err)
	}
	plan, err := w.tr.Plan(t.Context(), Options{})
	if err != nil || plan.Stale == 0 || plan.Invalidated != 0 || plan.Selected != 0 || plan.StaleEstimate.Calls == 0 {
		t.Fatalf("stale decisions are disclosed with their cost, nothing selected: %v %+v", err, plan)
	}
	if rep := w.run(Options{}); rep.Decided != 0 || w.dec.count() != paid {
		t.Fatalf("a derived-data change must cost nothing: %d calls, %+v", w.dec.count()-paid, rep)
	}
	v, _ := w.tr.Read(t.Context())
	if r := v.Results[row]; len(r.Values) == 0 || !r.Stale {
		t.Fatalf("the paid answers are still served, flagged stale: %+v", r)
	}
	// The user's explicit action re-decides them, at the disclosed cost.
	redo := w.run(Options{Redecide: true})
	if redo.Plan.Selected != plan.Stale || w.dec.count()-paid != plan.StaleEstimate.Calls {
		t.Fatalf("redecide: %d calls for %d stale (estimate %d): %+v", w.dec.count()-paid, plan.Stale, plan.StaleEstimate.Calls, redo.Plan)
	}
	if again, _ := w.tr.Plan(t.Context(), Options{}); again.Stale != 0 || again.Pending != 0 {
		t.Fatalf("after the re-decision nothing is stale: %+v", again)
	}
	// The message's own change still re-decides it (a new revision).
	m, _ := w.st.MessageByRow(t.Context(), row)
	m.Body = "Changed text?"
	w.write(m)
	if plan, _ := w.tr.Plan(t.Context(), Options{}); plan.Invalidated != 1 || plan.Selected != 1 {
		t.Fatalf("a changed message is decided again: %+v", plan)
	}
}
