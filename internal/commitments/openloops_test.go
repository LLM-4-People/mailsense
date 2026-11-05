package commitments

import (
	"fmt"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/agenda"
)

// The open-loop grouping view shares the one build with List: the same
// fixture proves the partition, the order, the item cap and the org-less
// exclusion. It lives beside the fixture, not inside the 1300-line
// service test.

// TestOpenLoopsGroupsTheDedupedSet: the grouped view is a pure re-index of
// the same deduped, sorted set List serves. Every organization carries the
// partition counts of its items, every kind key is present, the per-org item
// order is the list order, and an item without a counterparty is excluded.
func TestOpenLoopsGroupsTheDedupedSet(t *testing.T) {
	f := newFixture(t)
	f.st.setBook(testBook(t))
	f.st.setDeadlines(append(f.st.deadlineRows(), agenda.Deadline{
		MessageID: 13, ConversationID: "CF", Subject: "No counterparty",
		SenderName: "Dana Lee", Due: f.now.Add(2 * time.Hour),
	}))

	list, err := f.svc.List(t.Context(), Filter{Limit: MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	loops, err := f.svc.OpenLoops(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if loops.GeneratedAt != list.GeneratedAt {
		t.Fatalf("generated_at %q, want the list's %q", loops.GeneratedAt, list.GeneratedAt)
	}
	if loops.Total != 2 || len(loops.Organizations) != 2 {
		t.Fatalf("organizations %d/%d, want the two with a key", loops.Total, len(loops.Organizations))
	}

	// Org order: the organization with an overdue loop first, then the one
	// without. Items keep the global list order.
	atelier, delta := loops.Organizations[0], loops.Organizations[1]
	for _, tc := range []struct {
		name              string
		got               OrganizationOpenLoops
		key, label        string
		total, owner      int
		other, overdue    int
		storedDue         int
		dueKnd, replyKnd  int
		decideKnd, actKnd int
	}{
		{"atelier", atelier, "c:atelier nord", "Atelier Nord", 3, 3, 0, 1, 1, 1, 1, 1, 0},
		{"delta", delta, "d:delta.co", "delta.co", 2, 1, 1, 0, 1, 1, 1, 0, 0},
	} {
		if tc.got.Key != tc.key || tc.got.Label != tc.label {
			t.Fatalf("%s identity %+v", tc.name, tc.got)
		}
		if tc.got.Total != tc.total || tc.got.Owner != tc.owner || tc.got.Other != tc.other ||
			tc.got.Overdue != tc.overdue || tc.got.StoredDue != tc.storedDue {
			t.Fatalf("%s counts %+v", tc.name, tc.got)
		}
		if len(tc.got.Kinds) != len(Kinds) || tc.got.Kinds["due"] != tc.dueKnd ||
			tc.got.Kinds["reply"] != tc.replyKnd || tc.got.Kinds["decision"] != tc.decideKnd ||
			tc.got.Kinds["action"] != tc.actKnd {
			t.Fatalf("%s kinds %v", tc.name, tc.got.Kinds)
		}
		// The per-organization items are exactly the list rows of that
		// organization, in the list's own order.
		wantItems := 0
		iw := 0
		for _, c := range list.Items {
			if c.OrganizationKey != tc.got.Key {
				continue
			}
			wantItems++
			if iw >= len(tc.got.Items) || tc.got.Items[iw].ID != c.ID {
				t.Fatalf("%s item %d = %+v, want the list row %+v", tc.name, iw, tc.got.Items, c)
			}
			iw++
		}
		if len(tc.got.Items) != wantItems {
			t.Fatalf("%s items %d, want the list's %d", tc.name, len(tc.got.Items), wantItems)
		}
	}
	if atelier.Key != "c:atelier nord" || delta.Key != "d:delta.co" {
		t.Fatalf("org identity order %q then %q", atelier.Key, delta.Key)
	}
	// Five of the six list rows carry a counterparty; the sixth (message 13)
	// is excluded, never grouped under an empty key.
	grouped := 0
	for _, org := range loops.Organizations {
		if org.Key == "" {
			t.Fatalf("an item without a counterparty became an organization: %+v", org)
		}
		grouped += org.Total
	}
	if grouped != len(list.Items)-1 {
		t.Fatalf("grouped %d items, want %d of %d", grouped, len(list.Items)-1, len(list.Items))
	}
}

// One build serves both reads: OpenLoops after List reuses the cached set and
// does not touch the sources again.
func TestOpenLoopsSharesTheOneCachedBuild(t *testing.T) {
	f := newFixture(t)
	f.st.setBook(testBook(t))
	first, err := f.svc.List(t.Context(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	loops, err := f.svc.OpenLoops(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if loops.Total != 2 {
		t.Fatalf("organizations %d, want 2", loops.Total)
	}
	if got := f.threads.callCount(); got != 1 {
		t.Fatalf("OpenLoops rebuilt the set: thread resolver calls %d, want 1", got)
	}
	if _, _, book := f.st.calls(); book != 1 {
		t.Fatalf("OpenLoops reread the company book: %d calls, want 1", book)
	}
	if loops.GeneratedAt != first.GeneratedAt {
		t.Fatalf("generated_at %q, want the list's %q", loops.GeneratedAt, first.GeneratedAt)
	}
}

// The per-organization item list is capped, the counts are not: an
// organization with more than OrganizationItemCap loops reports its true
// partition and shows only the first page of the sorted set.
func TestOpenLoopsCapsItemsButNotCounts(t *testing.T) {
	f := newFixture(t)
	f.st.setBook(testBook(t))
	const n = OrganizationItemCap + 5
	deadlines := make([]agenda.Deadline, 0, n)
	for i := 0; i < n; i++ {
		deadlines = append(deadlines, agenda.Deadline{
			MessageID: int64(1000 + i), ConversationID: fmt.Sprintf("CC%d", i),
			Subject: fmt.Sprintf("Invoice %d", i), SenderName: "Alice",
			// Every date is past: all n loops are overdue, and the earliest
			// one is message 1000.
			Due:          f.now.Add(-time.Duration(n-i) * time.Minute),
			Participants: []string{"alice@atelier-nord.test"},
		})
	}
	f.st.setDeadlines(deadlines)
	f.threads.setStates(nil)
	f.decisions.setItems(nil)

	loops, err := f.svc.OpenLoops(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if loops.Total != 1 || len(loops.Organizations) != 1 {
		t.Fatalf("organizations %+v", loops.Organizations)
	}
	org := loops.Organizations[0]
	if org.Total != n || org.Owner != n || org.Other != 0 || org.Overdue != n || org.StoredDue != n {
		t.Fatalf("counts %+v", org)
	}
	if org.Kinds["due"] != n {
		t.Fatalf("kinds %v", org.Kinds)
	}
	if len(org.Items) != OrganizationItemCap {
		t.Fatalf("items %d, want the %d-item cap", len(org.Items), OrganizationItemCap)
	}
	if org.Items[0].MessageID != 1000 || org.Items[OrganizationItemCap-1].MessageID != int64(1000+OrganizationItemCap-1) {
		t.Fatalf("capped items %+v", org.Items)
	}
}

func TestOpenLoopsRequiresAStore(t *testing.T) {
	if _, err := New(Deps{}).OpenLoops(t.Context()); err == nil {
		t.Fatal("a missing store must fail")
	}
}
