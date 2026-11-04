package world

import (
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/fixturetest"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

// TestListRefreshInstallsUnderSampledGeneration guards the raced refresh: a
// write that lands while the list is being built must not discard the
// completed view. It is installed under the generation it sampled; the next
// read serves it and refreshes behind it for the newer generation.
func TestListRefreshInstallsUnderSampledGeneration(t *testing.T) {
	st, cfg := openWorldStore(t)
	seedWorldMail(t, st)
	now := worldTime(t, "2026-10-26T12:00:00Z")
	clock := clocktest.New(now)
	svc, err := New(Deps{Store: st, Config: cfg, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.List(t.Context(), ListQuery{Limit: 10}); err != nil {
		t.Fatal(err)
	}
	entry := readListEntry(svc)
	if entry == nil {
		t.Fatal("the first list was not cached")
	}
	sampled := entry.gen

	message := worldRecv("race-write", "race@newco.example", "Race", "2026-10-25T09:00:00Z",
		"C-race", "Written during the list refresh")
	if err := st.UpsertMessages(t.Context(), 0, []mail.Message{message}); err != nil {
		t.Fatal(err)
	}
	current := st.MailGeneration()
	if current == sampled {
		t.Fatal("the write did not move the mail generation")
	}

	clock.Advance(time.Second)
	// The refresh reads the store after the request that started it returned,
	// so it is one tracked task of the service's shutdown join.
	if !svc.refreshes.Track() {
		t.Fatal("a closed service admitted a refresh")
	}
	svc.refreshList(entry.limit, sampled, entry)
	state := readListCache(svc)
	if state.gen != sampled {
		t.Fatalf("the refresh installed under generation %d, want the sampled %d", state.gen, sampled)
	}
	if !state.at.Equal(now.Add(time.Second)) {
		t.Fatalf("the completed refresh was discarded: at %v", state.at)
	}

	// The next read serves the installed list immediately and refreshes for
	// the current generation.
	if _, err := svc.List(t.Context(), ListQuery{Limit: 10}); err != nil {
		t.Fatal(err)
	}
	fixturetest.WaitUntil(t, 2*time.Second, "list refresh for the newer generation", func() bool {
		state := readListCache(svc)
		return state.gen == current && !state.refreshing
	})
}

// readListEntry returns the installed cache entry, or nil. Tests need the
// pointer to drive refreshList as the List path would.
func readListEntry(svc *Service) *listEntry {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	return svc.list
}
