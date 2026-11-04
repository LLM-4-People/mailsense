package store

import (
	"slices"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
)

// TestOwnerContentIsTheContentBoundary: OwnerOnly is the classification
// population and legitimately includes owner-verdict mail in a picked shared
// store. A content read must use OwnerContent, so a single-message read can
// never return a subject or body the list, search, thread and attachment
// reads refuse to expose.
func TestOwnerContentIsTheContentBoundary(t *testing.T) {
	s, _ := open(t, nil)
	primary := mailtest.Message("src", "primary")
	shared := mailtest.Message("src", "shared")
	shared.StoreID = "store-2"

	a := answerMessage(t, s, primary)
	b := answerMessage(t, s, shared)
	answerVerdict(t, s, a, scope.Owner)
	answerVerdict(t, s, b, scope.Owner)
	// The scope verdict is per store and can mark a picked shared store's own
	// mailbox owner; the store kind is the content boundary.
	if _, err := s.db.ExecContext(t.Context(),
		"UPDATE folders SET store_kind = ? WHERE store_id = ?", string(mail.StoreShared), "store-2"); err != nil {
		t.Fatal(err)
	}

	collect := func(f MessageFilter) []int64 {
		t.Helper()
		var ids []int64
		if err := s.EachMessage(t.Context(), f, func(r *MessageRow) error {
			ids = append(ids, r.RowID)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return ids
	}

	owner := collect(MessageFilter{OwnerOnly: true})
	if !slices.Contains(owner, a) || !slices.Contains(owner, b) {
		t.Fatalf("OwnerOnly = %v, want both owner-verdict rows %d and %d", owner, a, b)
	}
	content := collect(MessageFilter{OwnerContent: true})
	if !slices.Contains(content, a) {
		t.Fatalf("OwnerContent = %v, want the primary row %d", content, a)
	}
	if slices.Contains(content, b) {
		t.Fatalf("OwnerContent = %v, leaked the shared-store row %d", content, b)
	}
	if got := collect(MessageFilter{OwnerContent: true, RowIDs: []int64{b}}); len(got) != 0 {
		t.Fatalf("a single-message content read returned the shared row %v", got)
	}
}
