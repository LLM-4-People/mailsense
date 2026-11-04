package evidence

import "testing"

// The deadline taxonomy calls a date already past at the message's own
// day overdue; date_next_days cannot see it (it emits only d >= 0), so
// date_past_days gives the nearest full date strictly before the day.
func TestDatePastDays(t *testing.T) {
	w := newWorld(t, everything, nil)
	past := msg("past", "ann@example.org", 0, "The invoice was due on 2026-10-05.")
	today := msg("today", "bob@example.org", 1, "The deadline is 2026-10-12.")
	future := msg("future", "carl@example.org", 2, "The deadline is 2026-10-20.")
	none := msg("none", "dora@example.org", 3, "No date here, thanks.")
	nearest := msg("nearest", "eve@example.org", 4, "First 2026-10-01, then 2026-10-10.")
	mixed := msg("mixed", "fay@example.org", 5, "Paid 2026-10-05; due 2026-10-20.")
	ambiguous := msg("ambiguous", "gil@example.org", 6, "The invoice was due on 03/04/2026.")
	cut := msg("cut", "hal@example.org", 7, "The invoice was due on 2026-10-05.")
	cut.BodyTruncated = true
	w.write(past, today, future, none, nearest, mixed, ambiguous, cut)
	w.run()

	want(t, w.record("past"), "date_past_days", Int(7))
	want(t, w.record("past"), "date_next_days", NotApplicable(ReasonNone)) // the gap this signal closes
	want(t, w.record("today"), "date_past_days", Int(0))                   // named, but not before the day
	want(t, w.record("future"), "date_past_days", Int(0))
	want(t, w.record("future"), "date_next_days", Int(8))
	want(t, w.record("none"), "date_past_days", NotApplicable(ReasonNone))
	want(t, w.record("nearest"), "date_past_days", Int(2)) // the nearest, not the first named
	want(t, w.record("mixed"), "date_past_days", Int(7))
	want(t, w.record("ambiguous"), "date_past_days", NotApplicable("ambiguous_order"))
	want(t, w.record("cut"), "date_past_days", Int(7).Cut(true))
	for _, id := range []string{"past", "today", "future", "none", "nearest", "mixed", "ambiguous", "cut"} {
		if v := w.record(id)["date_past_days"]; v.State == OK && v.Num < 0 {
			t.Errorf("%s: %+v: an overdue distance is never negative", id, v)
		}
	}
}
