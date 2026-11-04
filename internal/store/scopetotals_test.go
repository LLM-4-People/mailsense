package store

import (
	"reflect"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/scope"
)

// TestVerdictTotalsEqualThePerStoreSum protects the one total the sync report
// reads: summing the canonical per-store map must equal the mailbox-wide
// count, including unjudged, shared, unpicked and draft mail. VerdictCounts is
// now that sum, so this also pins the derivation against a per-store entry
// being dropped or overwritten instead of added.
func TestVerdictTotalsEqualThePerStoreSum(t *testing.T) {
	s, _ := seedLatestThreadWorld(t)
	want, err := s.VerdictCounts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	byStore, err := s.VerdictCountsByStore(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got := map[scope.Verdict]int{}
	for _, counts := range byStore {
		for verdict, n := range counts {
			got[verdict] += n
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("per-store sum %v differs from VerdictCounts %v", got, want)
	}
}
