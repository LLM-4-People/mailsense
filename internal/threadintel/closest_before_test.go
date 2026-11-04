package threadintel

import (
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/scope"
)

// TestClosestBeforeBreaksTimestampTiesById: both finders now share one rule.
// An earlier message at the same instant is still "before" only when its row
// id is lower, and the nearest one wins, so a reply never pairs with a later
// message that merely shares its timestamp.
func TestClosestBeforeBreaksTimestampTiesById(t *testing.T) {
	at := time.Date(2026, 3, 2, 14, 0, 0, 0, time.UTC)
	outbound := func(id int64) Message {
		return Message{RowID: id, At: at, Verdict: scope.Owner, Reason: scope.ReasonSentByOwner}
	}
	msgs := []Message{
		{RowID: 1, At: at},
		{RowID: 2, At: at},
		outbound(3),
		{RowID: 4, At: at},
	}
	m := &msgs[3] // row 4, same instant as the rest
	if got := lastInboundBefore(msgs, m); got == nil || got.RowID != 2 {
		t.Fatalf("inbound before row 4 = %+v, want row 2 (nearest lower id, same instant)", got)
	}
	if got := lastOutboundBefore(msgs, m); got == nil || got.RowID != 3 {
		t.Fatalf("outbound before row 4 = %+v, want row 3", got)
	}
	// A message with no earlier counterpart of its direction has none.
	if got := lastInboundBefore(msgs, &msgs[0]); got != nil {
		t.Fatalf("inbound before the first row = %+v, want nil", got)
	}
	if got := lastOutboundBefore(msgs, &msgs[2]); got != nil {
		t.Fatalf("outbound before the first outbound = %+v, want nil", got)
	}
}

// TestStateCountsAlwaysShowsTheWholeVocabulary: a counts map that drops a
// state with no thread makes one screen look like it has fewer kinds of
// conversation than another. Every surface fills from this owner instead.
func TestStateCountsAlwaysShowsTheWholeVocabulary(t *testing.T) {
	empty := StateCounts(nil)
	if len(empty) != len(States) {
		t.Fatalf("StateCounts(nil) has %d keys, want the %d states", len(empty), len(States))
	}
	for _, state := range States {
		if n, ok := empty[string(state)]; !ok || n != 0 {
			t.Fatalf("StateCounts(nil)[%q] = %d, %v; want a present zero", state, n, ok)
		}
	}
	sparse := StateCounts(map[string]int{string(AwaitingOwner): 3, "invented": 9})
	if sparse[string(AwaitingOwner)] != 3 {
		t.Fatalf("a present count was dropped: %#v", sparse)
	}
	if _, ok := sparse["invented"]; ok {
		t.Fatalf("an unknown key was invented onto the wire: %#v", sparse)
	}
	if sparse[string(NoAction)] != 0 {
		t.Fatalf("a missing state is not zero: %#v", sparse)
	}
}
