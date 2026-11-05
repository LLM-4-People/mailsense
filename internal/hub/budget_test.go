package hub

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/store"
)

// The hub budgets admit a plausible worst case: a few hundred label counts
// and a full recent-answers page stay under each bound, so the byte bound
// never evicts a normal screen. A budget reduced below these maxima must
// make the choice explicit rather than silently rebuild every read.
func TestHubCacheBudgetsAdmitTheirPages(t *testing.T) {
	counts := labelCountsValue{
		counts: make([]store.LabelCount, 200),
		review: map[string]int{"blocked": 1},
	}
	if got := weighLabelCounts(counts); got > hubLabelCountsMaxBytes {
		t.Fatalf("200 label counts weigh %d bytes, over the %d-byte budget", got, hubLabelCountsMaxBytes)
	}
	if got := weighMailboxCount(mailboxCountValue{}); got > hubMailboxMaxBytes {
		t.Fatalf("the mailbox counts weigh %d bytes, over the %d-byte budget", got, hubMailboxMaxBytes)
	}
	page := AnswerPage{Items: make([]Answer, store.AnswerMaxLimit)}
	if got := weighAnswerPage(page); got > hubRecentMaxBytes {
		t.Fatalf("a %d-answer page weighs %d bytes, over the %d-byte budget", store.AnswerMaxLimit, got, hubRecentMaxBytes)
	}
}
