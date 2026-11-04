package commitments

import (
	"reflect"
	"slices"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/action"
	"github.com/LLM-4-People/mailsense/internal/priority"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// The decision and action sources are two views of one ranked row, so they
// must build one shape. For each counterpart case the two items may differ
// only in the evidence family (kind), the id hashed from it and the row's own
// action class; every message-derived field, the display-name fallback
// included, is shared.
func TestRankedSourcesBuildOneSharedCommitmentShape(t *testing.T) {
	now := mustTime("2026-11-03T09:00:00Z")
	book := testBook(t)
	cases := []struct {
		name         string
		book         *store.CompanyBook
		senderName   string
		senderAddr   string
		wantPerson   string
		wantOrgKey   string
		wantOrgLabel string
	}{
		{"declared organization", book, "Alice Martin", "alice@atelier-nord.test",
			"alice@atelier-nord.test", "c:atelier nord", "Atelier Nord"},
		{"shared domain names the address without an organization", book, "Personal", "personal@gmail.com",
			"personal@gmail.com", "", ""},
		{"no book still names the address", nil, "Bob", "bob@delta.co",
			"bob@delta.co", "", ""},
		{"no address falls back to the display name", book, "Dana Lee", "",
			"Dana Lee", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := priority.Item{
				MessageID: 71, ConversationID: "C71", Subject: "Settle the account",
				SenderName: tc.senderName, SenderAddress: tc.senderAddr,
				ReceivedAt: stamp(now),
				Signals:    priority.Signals{Subject: subjectOf("billing_account", "payment")},
			}
			// The two rows differ only in the action class; every other value
			// is the same evidence, so any field the sources build differently
			// shows up as a mismatch below.
			decide := row
			decide.Action = action.Class{Primary: action.Decide, Waiting: action.WaitingOwner,
				DueSource: action.DueClassifier, DueLevel: 3}
			pay := row
			pay.Action = action.Class{Primary: action.Pay, Waiting: action.WaitingOwner,
				DueSource: action.DueClassifier, DueLevel: 3}

			svc := New(Deps{Store: &fakeStore{},
				Decisions: &fakeDecisions{items: []priority.Item{decide}},
				Actions:   &fakeActions{items: []priority.Item{pay}}})
			decisions, err := svc.decisionItems(t.Context(), tc.book)
			if err != nil || len(decisions) != 1 {
				t.Fatalf("decision items %d, err %v", len(decisions), err)
			}
			actions, err := svc.actionItems(t.Context(), tc.book)
			if err != nil || len(actions) != 1 {
				t.Fatalf("action items %d, err %v", len(actions), err)
			}
			dec, act := decisions[0], actions[0]

			// Kind-specific: the evidence family and the id hashed from it.
			if dec.Kind != KindDecision || act.Kind != KindAction {
				t.Fatalf("kinds %q and %q", dec.Kind, act.Kind)
			}
			if dec.ID == act.ID {
				t.Fatalf("both kinds share id %q", dec.ID)
			}
			if want := itemID(KindDecision, DirectionOwner, row.MessageID, row.ConversationID, ""); dec.ID != want {
				t.Errorf("decision id %q, want %q", dec.ID, want)
			}
			if want := itemID(KindAction, DirectionOwner, row.MessageID, row.ConversationID, ""); act.ID != want {
				t.Errorf("action id %q, want %q", act.ID, want)
			}

			// Ranked evidence is always an open obligation of the owner.
			if dec.Direction != DirectionOwner || act.Direction != DirectionOwner ||
				dec.Status != StatusOpen || act.Status != StatusOpen {
				t.Errorf("ranked invariants: decision %q/%q, action %q/%q",
					dec.Direction, dec.Status, act.Direction, act.Status)
			}

			// Shared: every field the row and the book decide. Asserting them
			// against the row itself (not only against each other) means a
			// builder that drops one cannot pass by leaving both zero.
			wantShared := Commitment{
				Direction:         DirectionOwner,
				Status:            StatusOpen,
				Title:             row.Subject,
				Subject:           row.Subject,
				ConversationID:    row.ConversationID,
				MessageID:         row.MessageID,
				OrganizationKey:   tc.wantOrgKey,
				OrganizationLabel: tc.wantOrgLabel,
				Person:            tc.wantPerson,
				LastAt:            row.ReceivedAt,
				Evidence:          []int64{row.MessageID},
				Waiting:           string(decide.Action.Waiting),
				DueSource:         string(decide.Action.DueSource),
				DueLevel:          decide.Action.DueLevel,
				Topic:             row.Signals.Subject.Topic,
				Subtopic:          row.Signals.Subject.Subtopic,
			}
			for _, c := range []struct {
				source string
				item   Commitment
			}{{"decision", dec}, {"action", act}} {
				// Kind, id and action are the kind-specific fields; zero them
				// to compare the shape both sources must share.
				shared := c.item
				shared.ID, shared.Kind, shared.Action = "", "", ""
				if !reflect.DeepEqual(shared, wantShared) {
					t.Errorf("%s shared shape %+v, want %+v", c.source, shared, wantShared)
				}
			}

			// The two kinds must agree on that shared shape exactly.
			decShared, actShared := dec, act
			decShared.ID, decShared.Kind, decShared.Action = "", "", ""
			actShared.ID, actShared.Kind, actShared.Action = "", "", ""
			if !reflect.DeepEqual(decShared, actShared) {
				t.Errorf("decision shape %+v, action shape %+v", decShared, actShared)
			}
			if !slices.Equal(dec.Evidence, act.Evidence) {
				t.Errorf("evidence: decision %v, action %v", dec.Evidence, act.Evidence)
			}

			// The action class stays the source row's answer for either kind.
			if dec.Action != string(decide.Action.Primary) || act.Action != string(pay.Action.Primary) {
				t.Errorf("actions %q and %q, want %q and %q",
					dec.Action, act.Action, decide.Action.Primary, pay.Action.Primary)
			}
		})
	}
}
