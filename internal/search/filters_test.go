package search

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

// allowedIDs is the filter set the searcher would rank against.
func allowedIDs(t *testing.T, s *Searcher, f store.SearchFilter) []int64 {
	t.Helper()
	ids, err := s.allowed(t.Context(), Query{Filter: f})
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func scans(s *Searcher) uint64 { return s.filterScans.Load() }

// A new message joins the cached set from a row-id range read: the first
// search after a sync keeps the ids it already had and adds the new one
// without listing the mailbox again. The next search finds the new message.
func TestFilterSetExtendsWithNewMail(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	w.index()
	if ids := allowedIDs(t, w.s, store.SearchFilter{}); len(ids) != 3 || scans(w.s) != 1 {
		t.Fatalf("first set: %d ids, %d scans", len(ids), scans(w.s))
	}
	w.mail("new", mail.RoleInbox, "Quarterly invoice", "another invoice")
	res := w.search(Query{Text: "quarterly invoice"})
	if !slices.Contains(w.order(res), "new") {
		t.Fatalf("the new message is not in the results: %v", w.order(res))
	}
	ids := allowedIDs(t, w.s, store.SearchFilter{})
	if scans(w.s) != 1 {
		t.Fatalf("the new message forced a full filter scan (%d scans)", scans(w.s))
	}
	if len(ids) != 4 || !slices.Contains(ids, w.ids["new"]) {
		t.Fatalf("the set did not grow with the new message: %v", ids)
	}
	for _, id := range []string{"both", "vec", "none"} {
		if !slices.Contains(ids, w.ids[id]) {
			t.Fatalf("the extension dropped %q: %v", id, ids)
		}
	}
	w.s.mu.Lock()
	set, ok := w.s.filters[filterKey(store.SearchFilter{})]
	w.s.mu.Unlock()
	if !ok || set.extends != 1 || set.gen != w.st.MailGeneration() {
		t.Fatalf("the cached set was not extended in place: %+v", set)
	}
}

// A folder sync refreshes every folder row (timestamp, item count); that is
// not a visibility change and must not rebuild the set. Changing a role or
// the selection is, and must.
func TestFilterSetIgnoresFolderNoiseButFollowsVisibility(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	w.index()
	if ids := allowedIDs(t, w.s, store.SearchFilter{}); len(ids) != 3 || scans(w.s) != 1 {
		t.Fatalf("first set: %d ids, %d scans", len(ids), scans(w.s))
	}
	folder, err := w.st.UpsertFolder(t.Context(), mail.Folder{SourceID: "src", StoreID: "store-1", StoreName: "Amélie",
		Path: "inbox", Role: mail.RoleInbox, StoreKind: mail.StoreOther, ItemCount: 42})
	if err != nil || folder == 0 {
		t.Fatalf("folder: %d %v", folder, err)
	}
	if ids := allowedIDs(t, w.s, store.SearchFilter{}); len(ids) != 3 || scans(w.s) != 1 {
		t.Fatalf("folder metadata rebuilt the set: %d ids, %d scans", len(ids), scans(w.s))
	}
	// The store is unpicked: no folder is visible, so the set is rebuilt (and
	// stays an empty slice, never nil: nil means "no filter" to the scan).
	if err := w.cfg.Set(source.SettingSelected, []any{}); err != nil {
		t.Fatal(err)
	}
	ids := allowedIDs(t, w.s, store.SearchFilter{})
	if scans(w.s) != 2 || ids == nil || len(ids) != 0 {
		t.Fatalf("the selection change did not rebuild the set: %#v, %d scans", ids, scans(w.s))
	}
}

// A message-owned filter cannot be extended: a re-sync rewrites those
// columns in place, so the set is rebuilt and the new mail is still found.
func TestFilterSetRebuildsForAMessageOwnedFilter(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	w.index()
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f := store.SearchFilter{Since: since}
	if ids := allowedIDs(t, w.s, f); len(ids) != 3 || scans(w.s) != 1 {
		t.Fatalf("first set: %d ids, %d scans", len(ids), scans(w.s))
	}
	w.mail("new", mail.RoleInbox, "New mail", "a new message")
	ids := allowedIDs(t, w.s, f)
	if scans(w.s) != 2 || !slices.Contains(ids, w.ids["new"]) {
		t.Fatalf("a date filter was not rebuilt: %v, %d scans", ids, scans(w.s))
	}
}

// A scope verdict rewrite invalidates the owner-scoped set even though no
// message row changed (the verdict is not part of MailGeneration's mail).
func TestFilterSetRebuildsOnAVerdictWrite(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	for _, id := range []string{"both", "none"} {
		if err := w.st.WriteVerdicts(t.Context(), "test", []store.VerdictRow{{RowID: w.ids[id],
			Verdict: scope.Owner, Reason: scope.ReasonAddressedFlag}}); err != nil {
			t.Fatal(err)
		}
	}
	owner := store.SearchFilter{OwnerOnly: true}
	if ids := allowedIDs(t, w.s, owner); len(ids) != 2 || scans(w.s) != 1 {
		t.Fatalf("first set: %v, %d scans", ids, scans(w.s))
	}
	if err := w.st.WriteVerdicts(t.Context(), "test", []store.VerdictRow{{RowID: w.ids["both"],
		Verdict: scope.Shared, Reason: scope.ReasonNotAddressed}}); err != nil {
		t.Fatal(err)
	}
	ids := allowedIDs(t, w.s, owner)
	if scans(w.s) != 2 {
		t.Fatalf("the verdict rewrite did not rebuild the owner set (%d scans)", scans(w.s))
	}
	if slices.Contains(ids, w.ids["both"]) || !slices.Contains(ids, w.ids["none"]) {
		t.Fatalf("the owner set kept a message that became shared: %v", ids)
	}
}

// The semantic ranking may run over a set that only grew; the exact read is
// what keeps a candidate that left the filter out of the answer.
func TestExactDropsCandidatesThatLeftTheFilter(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	for _, id := range []string{"both", "vec"} {
		if err := w.st.WriteVerdicts(t.Context(), "test", []store.VerdictRow{{RowID: w.ids[id],
			Verdict: scope.Owner, Reason: scope.ReasonAddressedFlag}}); err != nil {
			t.Fatal(err)
		}
	}
	scored := []vector.Scored{{ID: w.ids["both"], Sim: 0.9}, {ID: w.ids["vec"], Sim: 0.5}}
	kept, err := w.s.exact(t.Context(), store.SearchFilter{OwnerOnly: true}, scored, nil)
	if err != nil || len(kept) != 2 {
		t.Fatalf("owner candidates: %v %v", kept, err)
	}
	if err := w.st.WriteVerdicts(t.Context(), "test", []store.VerdictRow{{RowID: w.ids["both"],
		Verdict: scope.Shared, Reason: scope.ReasonNotAddressed}}); err != nil {
		t.Fatal(err)
	}
	kept, err = w.s.exact(t.Context(), store.SearchFilter{OwnerOnly: true}, scored, nil)
	if err != nil || len(kept) != 1 || kept[0].ID != w.ids["vec"] {
		t.Fatalf("a shared candidate survived the owner read: %v %v", kept, err)
	}
	// A candidate the full-text ranking returned is exact by its SQL (the
	// same filter, read a moment earlier), so it is not read again; the
	// owner boundary still re-checks it at hydration.
	kept, err = w.s.exact(t.Context(), store.SearchFilter{OwnerOnly: true}, scored,
		map[int64]bool{w.ids["both"]: true, w.ids["vec"]: true})
	if err != nil || len(kept) != 2 {
		t.Fatalf("text candidates were re-read: %v %v", kept, err)
	}
}

// The owner-only set is warmed at startup (Prepare) like the zero filter and
// is extended, not rebuilt, by ordinary mail.
func TestPrepareWarmsBothInteractiveFilterSets(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	w.index()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := w.s.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if scans(w.s) != 2 {
		t.Fatalf("Prepare listed %d filter sets, want the two interactive ones", scans(w.s))
	}
	w.mail("new", mail.RoleInbox, "A new message", "new mail body")
	if err := w.s.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if scans(w.s) != 2 {
		t.Fatalf("a new message rebuilt the warmed filter sets (%d scans)", scans(w.s))
	}
}
