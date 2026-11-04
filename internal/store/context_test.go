package store

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
)

// ContextDetails is one read of every stored dimension Ask renders next to a
// message: conversation, addressees, labels, read state and importance. A
// manual label outranks the judge's answer to the same question, and a shared
// message stays outside the owner-facing boundary.
func TestContextDetailsReadsOwnerContext(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()

	m := mailtest.Message("src", "ctx")
	m.ConversationID = "C-ctx"
	m.IsRead = false
	m.Importance = mail.Int(2)
	m.Recipients = []mail.Recipient{
		{Kind: mail.To, Name: "Client", Address: "client@example.test"},
		{Kind: mail.Cc, Name: "Boss", Address: "boss@example.test"},
	}
	id := answerMessage(t, s, m)
	answerVerdict(t, s, id, scope.Owner)
	answerLabel(t, s, id, "category", `"work"`, LabelManual)
	answerLabel(t, s, id, "category", `"spam"`, LabelJudge)
	answerLabel(t, s, id, "urgency", `2`, LabelJudge)

	shared := mailtest.Message("src", "shared")
	shared.ConversationID = "C-shared"
	sharedID := answerMessage(t, s, shared)
	answerVerdict(t, s, sharedID, scope.Shared)
	answerLabel(t, s, sharedID, "category", `"work"`, LabelManual)

	// A picked shared store may mark its own mailbox owner's mail
	// verdict=owner. Its participants and labels still must not leave the
	// store: scope is per store, and the store kind is the content boundary.
	if _, err := s.UpsertFolder(ctx, mail.Folder{SourceID: "src", StoreID: "store-2",
		StoreName: "Shared", Path: "Inbox", Role: mail.RoleInbox, StoreKind: mail.StoreShared}); err != nil {
		t.Fatal(err)
	}
	delegate := mailtest.Message("src", "shared-store")
	delegate.StoreID, delegate.FolderPath = "store-2", "Inbox"
	delegate.ConversationID = "C-shared-store"
	delegate.Recipients = []mail.Recipient{{Kind: mail.To, Name: "Other", Address: "other@example.test"}}
	delegateID := answerMessage(t, s, delegate)
	answerVerdict(t, s, delegateID, scope.Owner)
	answerLabel(t, s, delegateID, "topic", `"secret"`, LabelJudge)

	// A draft is owner mail but not owner-facing content: it must never
	// become evidence, even when its scope verdict is owner.
	draft := mailtest.Message("src", "draft")
	draft.FolderRole, draft.FolderPath = mail.RoleDrafts, "Drafts"
	draft.ConversationID = "C-draft"
	draft.Recipients = []mail.Recipient{{Kind: mail.To, Name: "Draft", Address: "draft@example.test"}}
	draftID := answerMessage(t, s, draft)
	answerVerdict(t, s, draftID, scope.Owner)
	answerLabel(t, s, draftID, "category", `"work"`, LabelManual)

	// A message with no recipients or labels still has a context entry: an
	// empty slice and an empty map, never a missing dimension. An unset
	// importance column is the normal level, not low.
	bare := mailtest.Message("src", "bare")
	bare.Recipients, bare.ConversationID = nil, ""
	bare.Importance = mail.OptInt{}
	bareID := answerMessage(t, s, bare)
	answerVerdict(t, s, bareID, scope.Owner)

	// The low level is stored and read as its own value, distinct from the
	// unset default above.
	low := mailtest.Message("src", "low")
	low.Importance = mail.Int(0)
	lowID := answerMessage(t, s, low)
	answerVerdict(t, s, lowID, scope.Owner)

	details, err := s.ContextDetails(ctx, []int64{id, sharedID, delegateID, draftID, bareID, lowID})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := details[id]
	if !ok {
		t.Fatalf("owner message absent from %+v", details)
	}
	if got.ConversationID != "C-ctx" {
		t.Fatalf("conversation id %q", got.ConversationID)
	}
	if got.IsRead || got.Importance != 2 {
		t.Fatalf("read=%v importance=%d, want unread high", got.IsRead, got.Importance)
	}
	if len(got.Recipients) != 2 || got.Recipients[0].Address != "client@example.test" ||
		got.Recipients[1].Kind != mail.Cc {
		t.Fatalf("recipients %+v", got.Recipients)
	}
	if fact := got.Labels["category"]; fact != (LabelFact{Value: `"work"`, Source: LabelManual}) {
		t.Fatalf("category label %+v, want the manual value", fact)
	}
	if fact := got.Labels["urgency"]; fact.Value != "2" || fact.Source != LabelJudge {
		t.Fatalf("urgency label %+v", fact)
	}
	if bare, ok := details[bareID]; !ok || bare.ConversationID != "" || len(bare.Recipients) != 0 ||
		len(bare.Labels) != 0 || !bare.IsRead || bare.Importance != 1 {
		t.Fatalf("empty context %+v present=%v", bare, ok)
	}
	if low, ok := details[lowID]; !ok || !low.IsRead || low.Importance != 0 {
		t.Fatalf("low-importance context %+v present=%v", low, ok)
	}
	if _, ok := details[sharedID]; ok {
		t.Fatalf("shared message leaked into the context: %+v", details[sharedID])
	}
	if _, ok := details[delegateID]; ok {
		t.Fatalf("shared-store owner verdict leaked participants: %+v", details[delegateID])
	}
	if _, ok := details[draftID]; ok {
		t.Fatalf("a draft leaked into the context: %+v", details[draftID])
	}
}
