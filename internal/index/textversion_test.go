package index

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Review of the crash-safety round, #9 (failed first: 0 stale, nothing
// rechecked): a change of the cleaning rules changes the text every vector
// was computed from, yet vectors were only looked at again when their
// message was written — search mixed vectors of two cleanings. Each
// vector carries the version of its text (the cleaning data's digest); a
// new version rechecks every vector: confirmed where the cleaned text is
// the same (free), embedded again where it changed (local, free).
func TestANewCleaningRechecksEveryVector(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("a", mail.RoleInbox, 1, "first text")
	w.mail("b", mail.RoleInbox, 2, "second text")
	w.run()
	calls := w.fake.Fake.Calls()
	w.ix.textVersion = func() string { return "cleaning-v2" }
	cov, err := w.ix.Coverage(t.Context())
	if err != nil || cov.Stale != 2 || cov.Indexed != 0 {
		t.Fatalf("a new cleaning makes every vector stale until checked: %v %+v", err, cov)
	}
	rep := w.run()
	if rep.Selected != 2 || rep.Unchanged != 2 || rep.Embedded != 0 || w.fake.Fake.Calls() != calls || rep.After.Indexed != 2 {
		t.Fatalf("the same cleaned text is confirmed without a call: %+v", rep)
	}
	if again := w.run(); again.Selected != 0 {
		t.Fatalf("confirmed vectors carry the new version: %+v", again)
	}
}
