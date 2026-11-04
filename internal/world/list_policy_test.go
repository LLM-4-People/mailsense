package world

import (
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/fixturetest"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

// TestListServesOnlyASnapshotAtLeastAsWide pins the width rule that keeps the
// list out of viewcache: one cached ranking serves every narrower page, but a
// wider request rebuilds instead of being served from a narrower snapshot.
// viewcache.Get selects by exact key and cannot express either direction.
func TestListServesOnlyASnapshotAtLeastAsWide(t *testing.T) {
	st, cfg := openWorldStore(t)
	seedWorldMail(t, st)
	now := worldTime(t, "2026-10-26T12:00:00Z")
	clock := clocktest.New(now)
	svc, err := New(Deps{Store: st, Config: cfg, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}

	narrow, err := svc.List(t.Context(), ListQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if narrow.GeneratedAt != now.UTC().Format(time.RFC3339) {
		t.Fatalf("first list built at %q", narrow.GeneratedAt)
	}

	// A wider request is not a prefix, so the narrower snapshot cannot serve
	// it: the read pays a build and replaces the single entry.
	clock.Advance(time.Second)
	wider, err := svc.List(t.Context(), ListQuery{Limit: MaxCompanyLimit})
	if err != nil {
		t.Fatal(err)
	}
	want := now.Add(time.Second).UTC().Format(time.RFC3339)
	if wider.GeneratedAt != want {
		t.Fatalf("a wider request was served from the narrower snapshot: %q, want %q",
			wider.GeneratedAt, want)
	}

	// The wider snapshot now serves every narrower page.
	clock.Advance(time.Second)
	page, err := svc.List(t.Context(), ListQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if page.GeneratedAt != want || len(page.Companies) != 2 {
		t.Fatalf("a narrower page was not sliced from the wider snapshot: %+v", page)
	}
}

// TestListServesTheRacedRebuildAndRefreshesBehind pins the install policy
// viewcache.Keep rejects. A rebuild that sampled a generation and completed
// after a write moved it is still installed and served, and only the next read
// rebuilds for the newer generation. Discarding the raced scan would serve the
// older pre-write snapshot instead; that is the observable difference.
func TestListServesTheRacedRebuildAndRefreshesBehind(t *testing.T) {
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
	message := worldRecv("policy-write", "policy@newco.example", "Policy", "2026-10-25T09:00:00Z",
		"C-policy", "Written while the list was being rebuilt")
	if err := st.UpsertMessages(t.Context(), 0, []mail.Message{message}); err != nil {
		t.Fatal(err)
	}
	current := st.MailGeneration()
	if current == sampled {
		t.Fatal("the write did not move the mail generation")
	}

	clock.Advance(time.Second)
	if !svc.refreshes.Track() {
		t.Fatal("a fresh service refused a refresh")
	}
	// The rebuild samples the old generation and completes after the write,
	// exactly the race refreshList must not discard.
	svc.refreshList(entry.limit, sampled, entry)

	clock.Advance(time.Second)
	raced, err := svc.List(t.Context(), ListQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(time.Second).UTC().Format(time.RFC3339); raced.GeneratedAt != want {
		t.Fatalf("the raced rebuild was discarded: served %q, want %q", raced.GeneratedAt, want)
	}

	// The next read rebuilt behind the served snapshot for the current
	// generation, so a later read pays nothing.
	refreshed := now.Add(2 * time.Second)
	fixturetest.WaitUntil(t, 2*time.Second, "refresh for the current generation", func() bool {
		state := readListCache(svc)
		return state.gen == current && !state.refreshing && state.at.Equal(refreshed)
	})
	fresh, err := svc.List(t.Context(), ListQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := fresh.GeneratedAt; got != refreshed.UTC().Format(time.RFC3339) {
		t.Fatalf("the current-generation refresh was not served: %q", got)
	}
}
