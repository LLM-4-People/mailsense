package conversations

// Parity of the page wire on a packed fixture: 40 conversations spanning
// every state, dated and undated mail, external organizations, participants,
// snippets, cursor paging, text search and both preview rules. The golden
// records the behaviour the page served before the snapshot/page cache
// consolidation; the before/after HTTP capture on the 10k-conversation
// fixture matched it exactly. Regenerate with UPDATE_GOLDEN=1 only for an
// intentional wire change.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/contacts"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

const pageGoldenPath = "testdata/page_golden.json"

type pageGolden struct {
	Pages []Page  `json:"pages"`
	Facts []Facts `json:"facts"`
}

func packedPageFixture(t *testing.T) (*Service, *fakePreviewGate) {
	t.Helper()
	states := make(map[string]threadintel.Assessment, 40)
	for i := range 40 {
		id := fmt.Sprintf("C%02d", i+1)
		states[id] = stateFor(id, threadintel.States[i%len(threadintel.States)])
	}
	svc, st, _, _ := newTestService(t, &fakePriority{states: states})
	if err := st.SaveReported(t.Context(), store.StoreRef{SourceID: "src", StoreID: "store-1"},
		[]string{"alex@acme.test"}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceContacts(t.Context(), store.StoreRef{SourceID: "src", StoreID: "store-1"}, "Contacts", []contacts.Contact{
		{EntryID: "alice", DisplayName: "Alice Martin", Company: "Atelier Nord", Emails: []string{"alice@atelier-nord.test"}},
		{EntryID: "bob", DisplayName: "Bob Stone", Company: "Beta Works", Emails: []string{"bob@beta.ca"}},
	}); err != nil {
		t.Fatal(err)
	}
	for i := range 40 {
		id := fmt.Sprintf("C%02d", i+1)
		subject := fmt.Sprintf("Budget item %02d", i+1)
		if i%2 == 1 {
			subject = fmt.Sprintf("Invoice item %02d", i+1)
		}
		instant := fmt.Sprintf("2026-11-%02dT%02d:00:00Z", (i%28)+1, (i%12)+1)
		if i == 39 {
			instant = ""
		}
		index := i
		seedConversation(t, st, fmt.Sprintf("m%02d", i+1), id, subject, instant, func(m *mail.Message) {
			m.Body = fmt.Sprintf("A short body for conversation %02d.", index+1)
			m.Recipients = []mail.Recipient{{Kind: mail.To, Name: "Owner", Address: "alex@acme.test"}}
			switch {
			case index%3 == 0:
				m.SenderName, m.SenderAddress = "Alice Martin", "alice@atelier-nord.test"
			case index%5 == 0:
				m.SenderName, m.SenderAddress = "Bob Stone", "bob@beta.ca"
			}
			m.SentRepresentingAddress = m.SenderAddress
			if index%7 == 0 {
				m.HasAttachments = true
				m.TaskDueDate = "2026-12-01T23:59:59Z"
				m.FlagStatus = mail.Int(0)
			}
		})
	}
	gate := &fakePreviewGate{}
	svc.gate = gate
	return svc, gate
}

// TestPageWireParityOnPackedFixture pins every field the page serves on the
// packed fixture. It is the regression guard for row order, total before the
// cursor, state chips/counts, enrichment, preview semantics, empty states and
// the cursor wire.
func TestPageWireParityOnPackedFixture(t *testing.T) {
	svc, gate := packedPageFixture(t)
	var got pageGolden

	queries := []Query{
		{Limit: 7},
		{Limit: 1},
		{Limit: 100},
		{Limit: 4, State: threadintel.AwaitingOwner},
		{Limit: 4, State: threadintel.AwaitingOther},
		{Limit: 4, State: threadintel.AnsweredByOwner},
		{Limit: 4, State: threadintel.AnsweredByOther},
		{Limit: 4, State: threadintel.NoAction},
		{Limit: 4, State: threadintel.Unknown},
		{Limit: 6, Q: "budget"},
		{Limit: 6, Q: "budget", State: threadintel.NoAction},
		{Limit: 6, Q: "zzzz-no-match"},
	}
	for _, q := range queries {
		page, err := svc.Page(t.Context(), q)
		if err != nil {
			t.Fatalf("query %+v: %v", q, err)
		}
		got.Pages = append(got.Pages, page)
		if q.Limit == 7 {
			next, err := svc.Page(t.Context(), Query{Limit: 7, Before: page.NextBefore})
			if err != nil {
				t.Fatalf("next page: %v", err)
			}
			got.Pages = append(got.Pages, next)
		}
	}
	// The identical repeat must serve the same page, not a rebuild.
	again, err := svc.Page(t.Context(), Query{Limit: 7})
	if err != nil {
		t.Fatal(err)
	}
	got.Pages = append(got.Pages, again)

	gate.preview = true
	preview, err := svc.Page(t.Context(), Query{Limit: 7})
	if err != nil {
		t.Fatal(err)
	}
	got.Pages = append(got.Pages, preview)
	gate.preview = false

	for _, tc := range []struct {
		since  string
		recent int
		orgs   int
	}{
		{"", 5, 3},
		{"2026-11-01T00:00:00Z", 5, 3},
		{"2026-11-20T00:00:00Z", 5, 3},
	} {
		facts, err := svc.Facts(t.Context(), tc.since, tc.recent, tc.orgs)
		if err != nil {
			t.Fatalf("facts %+v: %v", tc, err)
		}
		got.Facts = append(got.Facts, facts)
	}

	raw, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(filepath.Dir(pageGoldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pageGoldenPath, append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", pageGoldenPath)
		return
	}
	want, err := os.ReadFile(pageGoldenPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, bytes.TrimSuffix(want, []byte("\n"))) {
		t.Fatalf("the page wire drifted from %s; regenerate with UPDATE_GOLDEN=1 only for an intentional change", pageGoldenPath)
	}
}
