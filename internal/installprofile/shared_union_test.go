package installprofile

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/threads"
)

// TestSharedDomainsUnionsItsExtraEvidence: a domain learned from the thread
// graph and the same domain supplied by the address book appear once, and the
// result is sorted across both sources.
func TestSharedDomainsUnionsItsExtraEvidence(t *testing.T) {
	a := &threads.Analysis{
		Messages: []threads.Message{{
			Sender:     "ada@shared.test",
			Recipients: []string{"grace@shared.test", "owner@example.test"},
		}},
		Threads: []threads.Thread{{Members: []int{0}}},
	}
	rep := sharedDomains(a, map[string]bool{"owner@example.test": true}, 1, 1, "shared.test", "book.test")
	want := []string{"book.test", "shared.test"}
	if len(rep.Domains) != len(want) {
		t.Fatalf("domains = %v, want %v", rep.Domains, want)
	}
	for i := range want {
		if rep.Domains[i] != want[i] {
			t.Fatalf("domains = %v, want %v", rep.Domains, want)
		}
	}
	if rep.Senders != 1 || rep.Candidates != 1 || rep.Threads != 1 {
		t.Fatalf("report = %+v", rep)
	}
}
