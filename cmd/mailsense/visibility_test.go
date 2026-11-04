package main

import (
	"errors"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// TestMessagesGetUsesTheOwnerContentBoundary: the detail read is a content
// read, so it must use the same store-kind boundary as list, search, thread
// and attachment reads. OwnerOnly stays the classification population, which
// legitimately includes owner-verdict mail in a picked shared store.
func TestMessagesGetUsesTheOwnerContentBoundary(t *testing.T) {
	st, cfg, _ := firstRunWorld(t, nil)
	if err := cfg.Set(source.SettingSelected, mailtest.Picked([]string{"src"}, [2]string{"src", "store-2"})); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSource(t.Context(), "src", "test", "Test", source.Capabilities{}); err != nil {
		t.Fatal(err)
	}
	primary := mailtest.Message("src", "primary")
	shared := mailtest.Message("src", "shared")
	shared.StoreID, shared.StoreName = "store-2", "Shared"
	if err := st.UpsertMessages(t.Context(), 0, []mail.Message{primary, shared}); err != nil {
		t.Fatal(err)
	}
	ids, err := st.MessageIDs(t.Context(), store.MessageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("row ids = %v, want the primary and shared rows", ids)
	}
	for _, id := range ids {
		if err := st.WriteVerdicts(t.Context(), "test", []store.VerdictRow{{
			RowID: id, Verdict: scope.Owner, Reason: scope.ReasonAddressedFlag,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.UpsertFolder(t.Context(), mail.Folder{
		SourceID: "src", StoreID: "store-2", StoreName: "Shared", Path: "Inbox",
		Role: mail.RoleInbox, StoreKind: mail.StoreShared,
	}); err != nil {
		t.Fatal(err)
	}

	api := messagesAPI{w: &wire{app: &app{st: st}}}
	if view, err := api.Get(t.Context(), ids[0]); err != nil || view.RowID != ids[0] {
		t.Fatalf("primary read = %+v, %v", view, err)
	}
	if _, err := api.Get(t.Context(), ids[1]); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("shared-store read = %v, want ErrNotFound", err)
	}
}
