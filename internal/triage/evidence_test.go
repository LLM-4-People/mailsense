package triage

import (
	"context"
	"strings"
	"testing"
	"time"
)

// fakeEvidence hands out evidence facts by row.
type fakeEvidence struct {
	facts   map[int64]string
	version string
}

func (f *fakeEvidence) Facts(context.Context) (map[int64]string, error) { return f.facts, nil }
func (f *fakeEvidence) Version() string                                 { return f.version }

// The evidence facts join a state only while triage.facts.evidence is on
// (off by default: states and their hashes are unchanged); on without an
// engine is an error, never a state silently without them; a new
// evidence version rebuilds the states.
func TestEvidenceFactsJoinTheStateOnlyWhenOn(t *testing.T) {
	w := newWorld(t, nil)
	row := w.mail("ann@y.com", "c1", "Budget", 0, time.Hour)
	off := w.states()[row]
	ev := &fakeEvidence{facts: map[int64]string{row: "list_id: no"}, version: "1"}
	w.tr.UseEvidence(ev)
	if again := w.states()[row]; again != off || strings.Contains(off, "list_id") {
		t.Fatalf("off: the state must not change:\n%s", again)
	}
	if err := w.cfg.Set("triage.facts.evidence", true); err != nil {
		t.Fatal(err)
	}
	on := w.states()[row]
	if !strings.Contains(on, "list_id: no") || StateHash(on) == StateHash(off) {
		t.Fatalf("on: the facts join the state:\n%s", on)
	}
	// Regression (review L0, rules #15): on, the facts evidence owns come from it alone.
	for _, own := range []string{"question mark(s).", "characters)"} {
		if strings.Contains(off, own) == strings.Contains(on, own) || strings.Contains(on, own) {
			t.Errorf("on, %q comes from the evidence only:\n%s", own, on)
		}
	}
	if !strings.Contains(on, "Length: ") {
		t.Errorf("the shape of the text stays:\n%s", on)
	}
	ev.facts, ev.version = map[int64]string{row: "list_id: yes"}, "2"
	if s := w.states()[row]; !strings.Contains(s, "list_id: yes") {
		t.Fatalf("a new evidence version rebuilds the state:\n%s", s)
	}
	w.tr.UseEvidence(nil)
	if _, err := w.tr.prepare(t.Context()); err == nil || !strings.Contains(err.Error(), "no evidence engine") {
		t.Fatalf("on without an engine: %v", err)
	}
}
