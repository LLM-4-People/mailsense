package conversations

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

// The conversation budgets admit the mailbox scale the page base is built
// for: a ~10k-conversation digest snapshot and a page at MaxLimit rows both
// stay under their bounds, so the byte bound never evicts a normal mailbox.
func TestCacheBudgetsAdmitTheMailboxScale(t *testing.T) {
	snap := &snapshot{
		digests: make([]store.ConversationDigest, 10_000),
		states:  map[string]threadintel.Assessment{},
	}
	if got := weighSnapshot(snap); got > snapshotCacheMaxBytes {
		t.Fatalf("%d digests weigh %d bytes, over the %d-byte budget", len(snap.digests), got, snapshotCacheMaxBytes)
	}
	page := pageValue{view: Page{
		Conversations: make([]Row, MaxLimit),
		Counts:        map[string]int{},
	}}
	if got := weighPageValue(page); got > pageCacheMaxBytes {
		t.Fatalf("a %d-row page weighs %d bytes, over the %d-byte budget", MaxLimit, got, pageCacheMaxBytes)
	}
}
