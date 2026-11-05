package world

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/commitments"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// The profile budget admits a profile at every store cap, including the
// 2000-row open-thread list that dominates the estimate: the byte bound must
// not evict a profile the store would legitimately serve.
func TestProfileBudgetAdmitsACappedProfile(t *testing.T) {
	v := profileValue{view: ProfileView{
		Months:       make([]CompanyMonth, store.CompanyProfileMaxMonths),
		Recent:       make([]CompanyMessage, 20),
		OpenThreads:  make([]CompanyThread, 2000),
		ThreadStates: map[string]int{"open": 1},
		Events:       make([]CalendarEvent, 10),
		Deadlines:    make([]Deadline, 20),
		People:       make([]CompanyPerson, 100),
		Addresses:    make([]string, 100),
		Domains:      make([]string, 100),
		OpenLoops: &OrganizationOpenLoops{
			Items: make([]commitments.Commitment, 20),
			Kinds: map[string]int{},
		},
	}}
	if got := weighProfile(v); got > profileCacheMaxBytes {
		t.Fatalf("a capped profile weighs %d bytes, over the %d-byte budget", got, profileCacheMaxBytes)
	}
}
