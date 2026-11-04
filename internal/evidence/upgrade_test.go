package evidence

import (
	"slices"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/learning"
)

// Upgrade safety: a cached evidence record was written under a schema
// without a signal. The schema's effective versions decide reuse, so the
// new signal is recomputed and stored — never silently absent from the
// record the feature layout reads.
func TestANewSignalReachesCachedRecords(t *testing.T) {
	w := newWorld(t, everything, nil)
	all := Signals()
	old := slices.DeleteFunc(slices.Clone(all), func(s Signal) bool { return s.Name == "date_past_days" })
	if len(old) != len(all)-1 {
		t.Fatalf("the old registry: %d of %d signals", len(old), len(all))
	}
	w.ev.signals = old // the stored schema of a build before date_past_days
	w.write(msg("past", "ann@example.org", 0, "The invoice was due on 2026-10-05."))
	w.run()
	if _, ok := w.record("past")["date_past_days"]; ok {
		t.Fatal("a signal the stored schema lacks is in the record")
	}
	if st := w.status(); st.State != learning.Done {
		t.Fatalf("the old schema is complete: %+v", st)
	}
	w.ev.signals = all
	if st := w.status(); st.State == learning.Done {
		t.Fatalf("a new signal is a new evidence input: %+v", st)
	}
	rep := w.run()
	if rep.Computed == 0 || rep.Written == 0 {
		t.Fatalf("the run neither computed nor wrote the new signal: %+v", rep)
	}
	want(t, w.record("past"), "date_past_days", Int(7))
}
