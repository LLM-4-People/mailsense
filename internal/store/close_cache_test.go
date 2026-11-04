package store

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
)

// TestCloseReleasesEveryDerivedCache: a read after Close must fail at the
// closed pool, never answer from an in-memory population that nothing can
// refresh.
func TestCloseReleasesEveryDerivedCache(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	if err := s.UpsertMessages(ctx, 0, []mail.Message{mailtest.Message("src", "close-1")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MessageList(ctx, MessageListQuery{Counts: true, Limit: 10}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MessageList(ctx, MessageListQuery{Counts: true, Limit: 10}); err == nil {
		t.Fatal("a cached read was served after Close")
	}
	if s.counts != nil || s.routeTotals != nil || s.book.installed || s.counterparts.installed || s.cands != nil {
		t.Fatal("a derived cache survived Close")
	}
}
