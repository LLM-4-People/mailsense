package qa

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/priority"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// TestContextMessagesIsTheOneEnrichmentLoop pins the shared owner of the four
// planner-context legs. Company, thread summary and person used to spell the
// same loop: read the stored summaries, read the details and degrade to
// summary-only on failure, attach the ranking signals, build one numbered
// message per summary, then let the person leg drop other senders and add its
// display name. The table drives the same fixture through all three legs so a
// leg cannot drift from the others.
func TestContextMessagesIsTheOneEnrichmentLoop(t *testing.T) {
	legs := []struct {
		name     string
		reason   string
		logLabel string
		keep     func(map[int64]map[string]bool, string) func(int64, store.Summary) bool
		decorate func(name string) func(*contextMessage)
	}{
		{name: "company", reason: "company", logLabel: "qa company context unavailable"},
		{name: "thread summary", reason: "thread", logLabel: "qa summary context unavailable"},
		{name: "person", reason: "person", logLabel: "qa person context unavailable",
			keep: func(addresses map[int64]map[string]bool, _ string) func(int64, store.Summary) bool {
				return func(id int64, sum store.Summary) bool {
					return personSender(addresses[id], sum.SenderAddress)
				}
			},
			decorate: func(name string) func(*contextMessage) {
				return func(msg *contextMessage) { msg.Person = name }
			}},
	}

	scenarios := []struct {
		name       string
		details    bool // false omits every stored detail
		ids        []int64
		wantIDs    []int64
		wantKnown  []bool
		wantSignal []bool
	}{
		{
			name: "stored summary and detail", details: true,
			ids: []int64{1, 2}, wantIDs: []int64{1, 2}, wantKnown: []bool{true, true},
			wantSignal: []bool{true, false},
		},
		{
			name: "summary without detail", details: false,
			ids: []int64{1, 2}, wantIDs: []int64{1, 2}, wantKnown: []bool{false, false},
			wantSignal: []bool{true, false},
		},
		{
			name: "absent id is skipped", details: true,
			ids: []int64{1, 99, 2}, wantIDs: []int64{1, 2}, wantKnown: []bool{true, true},
			wantSignal: []bool{true, false},
		},
	}

	for _, leg := range legs {
		for _, scenario := range scenarios {
			t.Run(leg.name+"/"+scenario.name, func(t *testing.T) {
				w := newWorld(t, false)
				w.store.summaries = map[int64]store.Summary{
					1: {RowID: 1, Subject: "Invoice", SenderName: "Amelie", SenderAddress: "amelie@example.org",
						Instant: "2026-10-20T09:00:00Z", Role: mail.RoleInbox, Body: "body one"},
					2: {RowID: 2, Subject: "Re: Invoice", SenderName: "Other", SenderAddress: "other@example.org",
						Instant: "2026-10-21T09:00:00Z", Role: mail.RoleSent, Body: "body two"},
				}
				w.store.details = map[int64]store.ContextDetail{
					1: {Recipients: []mail.Recipient{{Name: "Owner", Address: "owner@example.org"}},
						Labels:         map[string]store.LabelFact{"category": {Value: "work"}},
						ConversationID: "C-1", IsRead: true, Importance: 2},
					2: {ConversationID: "C-2", IsRead: false, Importance: 1},
				}
				w.store.omitDetails = !scenario.details
				// Only message 1 carries a ranking signal: an absent signal is
				// nil, never a zero-valued one.
				w.prio.top = []priority.Item{{MessageID: 1, Signals: priority.Signals{Urgency: 2}}}

				addresses := map[int64]map[string]bool{
					1: {mail.NormalizeAddress("amelie@example.org"): true},
					2: {mail.NormalizeAddress("other@example.org"): true},
				}
				var keep func(int64, store.Summary) bool
				var decorate func(*contextMessage)
				if leg.keep != nil {
					keep = leg.keep(addresses, "Amelie")
				}
				if leg.decorate != nil {
					decorate = leg.decorate("Amelie")
				}

				msgs, err := w.svc.contextMessages(t.Context(), scenario.ids, leg.reason, leg.logLabel, keep, decorate)
				if err != nil {
					t.Fatal(err)
				}
				if len(msgs) != len(scenario.wantIDs) {
					t.Fatalf("messages %+v, want ids %v", msgs, scenario.wantIDs)
				}
				for i, msg := range msgs {
					if msg.ID != scenario.wantIDs[i] || msg.Reason != leg.reason {
						t.Fatalf("message %d = %+v, want id %d reason %q", i, msg, scenario.wantIDs[i], leg.reason)
					}
					if msg.ContextKnown != scenario.wantKnown[i] {
						t.Fatalf("message %d ContextKnown = %v, want %v", i, msg.ContextKnown, scenario.wantKnown[i])
					}
					if (msg.Signals != nil) != scenario.wantSignal[i] {
						t.Fatalf("message %d Signals = %+v, want present=%v", i, msg.Signals, scenario.wantSignal[i])
					}
					if leg.decorate != nil && msg.Person != "Amelie" {
						t.Fatalf("message %d Person = %q, want the leg's decoration", i, msg.Person)
					}
					if leg.decorate == nil && msg.Person != "" {
						t.Fatalf("message %d Person = %q, want none", i, msg.Person)
					}
				}
				// The enriched dimensions come from the one owner, so both
				// legs that keep message 1 must see identical stored facts.
				if scenario.details && msgs[0].ConversationID != "C-1" || scenario.details && !msgs[0].IsRead ||
					scenario.details && msgs[0].Importance != 2 || scenario.details && len(msgs[0].Labels) != 1 {
					t.Fatalf("message 1 lost its stored detail: %+v", msgs[0])
				}
				// One summaries read, one detail read and one ranking read per
				// call: the legs no longer scan separately.
				if got := w.store.summariesCalls; got != 1 {
					t.Fatalf("the leg read the summaries %d times, want 1", got)
				}
				if got := w.prio.topCalls; got != 1 {
					t.Fatalf("the leg read the ranking %d times, want 1", got)
				}
			})
		}
	}
}

// TestContextMessagesKeepsSummaryFactsWhenDetailsFail: a failed detail read is
// a warning, not a discarded leg. The stored message facts stay usable and
// nothing is invented for the missing dimensions.
func TestContextMessagesKeepsSummaryFactsWhenDetailsFail(t *testing.T) {
	w := newWorld(t, false)
	w.store.summaries = map[int64]store.Summary{7: {RowID: 7, Subject: "Kept", SenderAddress: "a@example.org"}}
	w.store.details = map[int64]store.ContextDetail{7: {IsRead: true, Importance: 2}}
	w.store.omitDetails = true

	msgs, err := w.svc.contextMessages(t.Context(), []int64{7}, "thread", "qa summary context unavailable", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Subject != "Kept" {
		t.Fatalf("messages %+v", msgs)
	}
	if msgs[0].ContextKnown || msgs[0].IsRead || msgs[0].Importance != 0 {
		t.Fatalf("a failed detail read invented stored facts: %+v", msgs[0])
	}
}

// TestContextMessagesAppliesTheLegFilterAndDecoration pins the two leg-only
// behaviours the shared loop must still honour: the person leg keeps only the
// messages the named person sent and stamps its display name on them, while a
// leg with no filter takes every stored summary.
func TestContextMessagesAppliesTheLegFilterAndDecoration(t *testing.T) {
	w := newWorld(t, false)
	w.store.summaries = map[int64]store.Summary{
		1: {RowID: 1, Subject: "from them", SenderAddress: "Amelie.Durand@example.org"},
		2: {RowID: 2, Subject: "from a colleague", SenderAddress: "colleague@example.org"},
	}
	addresses := map[int64]map[string]bool{
		1: {mail.NormalizeAddress("amelie.durand@example.org"): true},
		2: {mail.NormalizeAddress("colleague@example.org"): true},
	}

	msgs, err := w.svc.contextMessages(t.Context(), []int64{1, 2}, "person", "qa person context unavailable",
		func(id int64, sum store.Summary) bool { return personSender(addresses[id], sum.SenderAddress) },
		func(msg *contextMessage) { msg.Person = "Amelie Durand" })
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("the person leg kept %d messages, want both", len(msgs))
	}
	for _, msg := range msgs {
		if msg.Person != "Amelie Durand" {
			t.Fatalf("message %d not decorated: %+v", msg.ID, msg)
		}
	}

	// A message from someone else is not this person's evidence.
	addresses[2] = map[string]bool{}
	msgs, err = w.svc.contextMessages(t.Context(), []int64{1, 2}, "person", "qa person context unavailable",
		func(id int64, sum store.Summary) bool { return personSender(addresses[id], sum.SenderAddress) },
		func(msg *contextMessage) { msg.Person = "Amelie Durand" })
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].ID != 1 {
		t.Fatalf("the sender filter kept %+v, want only message 1", msgs)
	}
}
