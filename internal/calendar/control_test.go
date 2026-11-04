package calendar

import (
	"testing"
	"time"
)

// TestEventRejectsC1Control: the attendee check is the mail owner's
// unicode.IsControl predicate, so the C1 range (U+0080..U+009F) is refused like
// every other control character, not only 0x00..0x1F and DEL.
func TestEventRejectsC1Control(t *testing.T) {
	event := func(name string) Event {
		return Event{EntryID: "e1", SourceID: "s1", StoreKey: "k1",
			Start:     time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC),
			End:       time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC),
			Busy:      Busy,
			Attendees: []Attendee{{Address: "ada@example.test", Name: name, Kind: Required}}}
	}
	for _, r := range []rune{0x00, 0x1f, 0x7f, 0x80, 0x85, 0x9f} {
		if err := event("ada" + string(r)).Validate(); err == nil {
			t.Fatalf("a U+%04X control in an attendee name was accepted", r)
		}
	}
	if err := event("Ada Lovelace").Validate(); err != nil {
		t.Fatalf("a normal attendee was refused: %v", err)
	}
}
