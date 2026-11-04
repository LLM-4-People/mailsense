package qa

import (
	"reflect"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

// TestThreadEvidenceFromCopiesEveryAssessmentFact keeps the one projection of a
// thread assessment honest: every field threadintel.Assessment carries must
// reach the evidence block, or be listed here with the reason it is not an
// evidence dimension. Company and retrieval used to copy the fields by hand,
// so one leg could gain a fact the other silently dropped.
func TestThreadEvidenceFromCopiesEveryAssessmentFact(t *testing.T) {
	// Deliberately not evidence dimensions:
	//   ConversationID is the join key the callers already hold,
	//   ReplyToMessageID, Confidence, Topic and Subtopic are resolver internals
	//   this prompt never cited.
	ignored := map[string]bool{
		"ConversationID":   true,
		"ReplyToMessageID": true,
		"Confidence":       true,
		"Topic":            true,
		"Subtopic":         true,
	}

	assessment := threadintel.Assessment{
		ConversationID:     "C-1",
		State:              threadintel.AwaitingOwner,
		Reason:             threadintel.ReasonInboundLast,
		ActorName:          "Amelie",
		ActorAddress:       "amelie@example.org",
		LastAt:             "2026-10-21T09:00:00Z",
		LastMessageID:      42,
		OwnerActorName:     "Amelie",
		OwnerActorAddress:  "amelie@example.org",
		OwnerLastAt:        "2026-10-21T09:00:00Z",
		OwnerLastMessageID: 42,
		ReplyToMessageID:   41,
		Visibility:         threadintel.Complete,
		Confidence:         0.9,
		Messages:           3,
		Topic:              "invoice",
		Subtopic:           "payment",
	}
	evidence := threadEvidenceFrom(assessment, "Invoice", "2026-10-21T10:00:00Z")

	// Subject and LastAt are the caller's own facts, not the assessment's.
	if evidence.Subject != "Invoice" || evidence.LastAt != "2026-10-21T10:00:00Z" {
		t.Fatalf("caller facts lost: %+v", evidence)
	}

	value := reflect.ValueOf(evidence)
	types := reflect.TypeOf(assessment)
	for i := 0; i < types.NumField(); i++ {
		name := types.Field(i).Name
		if ignored[name] {
			continue
		}
		field, ok := value.Type().FieldByName(name)
		if !ok {
			// ActorAddress maps to ActorAddr, OwnerActorAddress to
			// OwnerActorAddr and LastMessageID to MessageID: the intentional
			// renames.
			switch name {
			case "ActorAddress":
				name = "ActorAddr"
			case "OwnerActorAddress":
				name = "OwnerActorAddr"
			case "LastMessageID":
				name = "MessageID"
			case "OwnerLastMessageID":
				name = "OwnerMessageID"
			default:
				t.Fatalf("assessment field %s has no evidence field and is not declared ignored", name)
			}
			field, ok = value.Type().FieldByName(name)
			if !ok {
				t.Fatalf("evidence has no field for assessment field %s", name)
			}
		}
		got := value.FieldByIndex(field.Index)
		if got.IsZero() {
			t.Errorf("assessment %s did not reach the evidence block", types.Field(i).Name)
		}
	}
}
