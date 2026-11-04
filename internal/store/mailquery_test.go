package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/contacts"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/mailquery"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
)

func mailQueryVerdict(t *testing.T, s *Store, id int64, verdict scope.Verdict, reason scope.Reason) {
	t.Helper()
	if err := s.WriteVerdicts(t.Context(), "test", []VerdictRow{{RowID: id, Verdict: verdict, Reason: reason}}); err != nil {
		t.Fatal(err)
	}
}

func sent(id string, when string, to ...mail.Recipient) mail.Message {
	m := mailtest.Message("src", id)
	m.FolderRole, m.FolderPath = mail.RoleSent, "Sent"
	m.SenderName, m.SenderAddress, m.SentRepresentingAddress = "Amélie Durand", mailtest.Owner, mailtest.Owner
	m.AddressedToMe = mail.AddressedNo
	m.Recipients = to
	m.SentOn, m.ReceivedAt = when, when
	m.Subject = "Sent " + id
	m.Body = "Synthetic sent body " + id
	m.ConversationID = "C-" + id
	return m
}

// seedContactMailbox builds a deterministic mix of received, sent, shared
// and unpicked mail. Ada: 2 received + 2 sent (one shared with Linus),
// Grace: 1 received + 1 sent, Linus: 1 sent, plus one sent only to the
// owner and one shared/unpicked message that must never count.
func seedContactMailbox(t *testing.T, s *Store) map[string]int64 {
	t.Helper()
	if err := s.SaveReported(t.Context(), StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	recv := func(id, from, name, when, conv string) mail.Message {
		m := mailtest.Message("src", id)
		m.SenderName, m.SenderAddress = name, from
		m.ReceivedAt, m.SentOn = when, when
		m.Subject = "Received " + id
		m.Body = "Synthetic received body " + id
		m.ConversationID = conv
		m.IsRead = true
		m.HasAttachments = false
		return m
	}
	messages := []mail.Message{
		recv("a1", "ada@example.com", "Ada Lovelace", "2026-10-20T09:00:00Z", "C-ada-1"),
		recv("a2", "ada@example.com", "Ada Lovelace", "2026-10-21T09:00:00Z", "C-ada-2"),
		recv("g1", "grace@example.com", "Grace Hopper", "2026-10-22T09:00:00Z", "C-grace"),
		sent("a3", "2026-10-23T09:00:00Z", mail.Recipient{Kind: mail.To, Name: "Ada Lovelace", Address: "ada@example.com"}),
		sent("g2", "2026-10-24T09:00:00Z", mail.Recipient{Kind: mail.To, Name: "Grace Hopper", Address: "grace@example.com"}),
		sent("al1", "2026-10-25T09:00:00Z",
			mail.Recipient{Kind: mail.To, Name: "Ada Lovelace", Address: "ada@example.com"},
			mail.Recipient{Kind: mail.Cc, Name: "Linus Torvalds", Address: "linus@example.com"}),
		recv("shared", "ada@example.com", "Ada Lovelace", "2026-10-26T09:00:00Z", "C-shared"),
		recv("unpicked", "ada@example.com", "Ada Lovelace", "2026-10-27T09:00:00Z", "C-unpicked"),
		sent("self", "2026-10-28T09:00:00Z", mail.Recipient{Kind: mail.To, Name: "Amélie", Address: mailtest.Owner}),
	}
	messages[6].ConversationID = "C-shared"
	messages[7].StoreID = "store-3" // not picked by open()
	messages[8].Subject = "Sent to myself"
	ids := make(map[string]int64, len(messages))
	for _, m := range messages {
		ids[m.ID] = answerMessage(t, s, m)
	}
	for _, id := range []string{"a1", "a2", "g1"} {
		mailQueryVerdict(t, s, ids[id], scope.Owner, scope.ReasonRecipientMatch)
	}
	for _, id := range []string{"a3", "g2", "al1", "self"} {
		mailQueryVerdict(t, s, ids[id], scope.Owner, scope.ReasonSentByOwner)
	}
	mailQueryVerdict(t, s, ids["shared"], scope.Shared, scope.ReasonRecipientMatch)
	mailQueryVerdict(t, s, ids["unpicked"], scope.Owner, scope.ReasonRecipientMatch)
	return ids
}

// seedSharedStoreMessage seeds one message in a picked shared store whose
// own mailbox marks it verdict=owner. Counts, lists, time buckets and Ask
// context must all ignore its subject, sender and participants.
func seedSharedStoreMessage(t *testing.T, s *Store) int64 {
	t.Helper()
	if _, err := s.UpsertFolder(t.Context(), mail.Folder{SourceID: "src", StoreID: "store-2",
		StoreName: "Shared", Path: "Inbox", Role: mail.RoleInbox, StoreKind: mail.StoreShared}); err != nil {
		t.Fatal(err)
	}
	m := mailtest.Message("src", "shared-owner")
	m.StoreID, m.FolderPath = "store-2", "Inbox"
	m.SenderName, m.SenderAddress = "Hidden", "hidden@secret.example"
	m.ReceivedAt, m.SentOn = "2026-10-29T09:00:00Z", "2026-10-29T09:00:00Z"
	m.TaskDueDate = "2026-11-03T16:00:00Z"
	m.Subject = "Shared content must not count"
	m.Body = "Shared body must not count"
	m.ConversationID = "C1"
	id := answerMessage(t, s, m)
	mailQueryVerdict(t, s, id, scope.Owner, scope.ReasonRecipientMatch)
	return id
}

// seedOwnerDraft seeds one owner-verdict draft. Drafts are owner mail but
// not owner-facing content: no read may count, cite or prompt from it.
func seedOwnerDraft(t *testing.T, s *Store) int64 {
	t.Helper()
	m := mailtest.Message("src", "draft-owner")
	m.FolderRole, m.FolderPath = mail.RoleDrafts, "Drafts"
	m.SenderName, m.SenderAddress = "Alex Morgan", mailtest.Owner
	m.ReceivedAt, m.SentOn = "2026-10-30T09:00:00Z", "2026-10-30T09:00:00Z"
	m.Subject = "Draft content must not count"
	m.Body = "Draft body must not count"
	id := answerMessage(t, s, m)
	mailQueryVerdict(t, s, id, scope.Owner, scope.ReasonSentByOwner)
	return id
}

func contactPlan() mailquery.Plan {
	return mailquery.Plan{Kind: mailquery.KindContacts, Action: mailquery.ActionRank,
		Direction: mailquery.DirectionBoth, Metric: mailquery.MetricMessages,
		TimeRange: mailquery.TimeAll, Limit: 10, Order: mailquery.OrderDesc}
}

func TestMailQueryContactsRankMessagesAndThreads(t *testing.T) {
	s, _ := open(t, nil)
	seedContactMailbox(t, s)
	res, err := s.ExecuteMailQuery(t.Context(), contactPlan(), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 3 || res.Total != 7 {
		t.Fatalf("rows %d total %d: %+v", len(res.Rows), res.Total, res.Rows)
	}
	ada, grace, linus := res.Rows[0], res.Rows[1], res.Rows[2]
	if ada.Address != "ada@example.com" || ada.Label != "Ada Lovelace" || ada.Messages != 4 ||
		ada.Sent != 2 || ada.Received != 2 || ada.Threads != 4 || ada.LastAt != "2026-10-25T09:00:00Z" {
		t.Fatalf("Ada row %+v", ada)
	}
	if grace.Address != "grace@example.com" || grace.Messages != 2 || grace.Sent != 1 || grace.Received != 1 || grace.Threads != 2 {
		t.Fatalf("Grace row %+v", grace)
	}
	if linus.Address != "linus@example.com" || linus.Messages != 1 || linus.Sent != 1 || linus.Received != 0 {
		t.Fatalf("Linus row %+v", linus)
	}
	if res.Rows[0].Label == "" || res.Rows[0].Address == "" {
		t.Fatal("a contact row must carry a display label and an address")
	}
	for _, row := range res.Rows {
		if row.Address == mailtest.Owner {
			t.Fatalf("the owner leaked into the contact ranking: %+v", row)
		}
	}
}

func TestMailQueryContactsDirectionMetricAndContactFilter(t *testing.T) {
	s, _ := open(t, nil)
	seedContactMailbox(t, s)
	cases := []struct {
		name   string
		edit   func(*mailquery.Plan)
		order  []string
		counts []int
	}{
		{"sent", func(p *mailquery.Plan) { p.Direction = mailquery.DirectionSent },
			[]string{"ada@example.com", "linus@example.com", "grace@example.com"}, []int{2, 1, 1}},
		{"received", func(p *mailquery.Plan) { p.Direction = mailquery.DirectionReceived },
			[]string{"ada@example.com", "grace@example.com"}, []int{2, 1}},
		{"exchanges", func(p *mailquery.Plan) { p.Metric = mailquery.MetricExchanges },
			[]string{"ada@example.com", "grace@example.com"}, []int{4, 2}},
		{"threads", func(p *mailquery.Plan) { p.Metric = mailquery.MetricThreads },
			[]string{"ada@example.com", "grace@example.com", "linus@example.com"}, []int{4, 2, 1}},
		{"ada", func(p *mailquery.Plan) { p.Contact = "ada" },
			[]string{"ada@example.com"}, []int{4}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := contactPlan()
			c.edit(&p)
			res, err := s.ExecuteMailQuery(t.Context(), p.Normalize(), time.UTC)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Rows) != len(c.order) {
				t.Fatalf("rows %+v, want %v", res.Rows, c.order)
			}
			for i, addr := range c.order {
				if res.Rows[i].Address != addr || res.Rows[i].Messages != c.counts[i] {
					t.Fatalf("row %d = %+v, want %s/%d", i, res.Rows[i], addr, c.counts[i])
				}
			}
		})
	}
}

func companyPlan() mailquery.Plan {
	return mailquery.Plan{Kind: mailquery.KindCompanies, Action: mailquery.ActionRank,
		Direction: mailquery.DirectionBoth, Metric: mailquery.MetricMessages,
		TimeRange: mailquery.TimeAll, Limit: 15, Order: mailquery.OrderDesc}
}

// seedCompanyMailbox builds a mailbox where organizations are only partly
// declared: Atelier Nord has declared spellings on its domain, Beta Inc merges
// two corporate domains and one declared free-mail address, Delta is a plain
// domain, and both a personal free-mail sender and the owner's organization
// must never be ranked.
func seedCompanyMailbox(t *testing.T, s *Store) map[string]int64 {
	t.Helper()
	ref := StoreRef{SourceID: "src", StoreID: "store-1"}
	if err := s.SaveReported(t.Context(), ref, []string{"alex@acme.test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceContacts(t.Context(), ref, "Contacts", []contacts.Contact{
		{EntryID: "alice", DisplayName: "Alice", Company: "Atelier Nord", Emails: []string{"alice@atelier-nord.test"}},
		{EntryID: "gina", DisplayName: "Gina", Company: "Atelier Nord", Emails: []string{"gina@atelier-nord.test"}},
		{EntryID: "frank", DisplayName: "Frank", Company: "Atelier Nord Inc", Emails: []string{"frank@atelier-nord.test"}},
		{EntryID: "carol", DisplayName: "Carol", Company: "Beta Inc", Emails: []string{"carol@beta.test"}},
		{EntryID: "dave", DisplayName: "Dave", Company: "Beta Inc", Emails: []string{"dave@gamma.dev"}},
		{EntryID: "gmail-beta", DisplayName: "Gmail Beta", Company: "Beta Inc", Emails: []string{"beta.person@gmail.com"}},
		{EntryID: "eve", DisplayName: "Eve", Company: "Acme", Emails: []string{"eve@acme.test"}},
		{EntryID: "contractor", DisplayName: "Contractor", Company: "Acme", Emails: []string{"contractor@gmail.com"}},
	}); err != nil {
		t.Fatal(err)
	}
	recv := func(id, from, name, when, conv string) mail.Message {
		m := mailtest.Message("src", id)
		m.SenderName, m.SenderAddress = name, from
		m.ReceivedAt, m.SentOn = when, when
		m.Subject = "Received " + id
		m.Body = "Synthetic received body " + id
		m.ConversationID = conv
		m.IsRead = true
		m.HasAttachments = false
		return m
	}
	messages := []mail.Message{
		recv("a1", "alice@atelier-nord.test", "Alice", "2026-10-01T09:00:00Z", "C-art-1"),
		sent("a2", "2026-10-02T09:00:00Z",
			mail.Recipient{Kind: mail.To, Name: "Alice", Address: "alice@atelier-nord.test"},
			mail.Recipient{Kind: mail.To, Name: "Bob", Address: "bob@atelier-nord.test"}),
		recv("a3", "bob@atelier-nord.test", "Bob", "2026-10-03T09:00:00Z", "C-art-3"),
		recv("g1", "personal@gmail.com", "Personal", "2026-10-04T09:00:00Z", "C-gmail"),
		recv("b1", "beta.person@gmail.com", "Beta Person", "2026-10-05T09:00:00Z", "C-beta-1"),
		sent("b2", "2026-10-06T09:00:00Z", mail.Recipient{Kind: mail.To, Name: "Carol", Address: "carol@beta.test"}),
		sent("b3", "2026-10-07T09:00:00Z", mail.Recipient{Kind: mail.To, Name: "Carol", Address: "carol@beta.test"}),
		recv("b4", "dave@gamma.dev", "Dave", "2026-10-08T09:00:00Z", "C-beta-4"),
		recv("o1", "eve@acme.test", "Eve", "2026-10-09T09:00:00Z", "C-acme-1"),
		sent("o2", "2026-10-10T09:00:00Z", mail.Recipient{Kind: mail.To, Name: "Eve", Address: "eve@acme.test"}),
		recv("d1", "delta@delta.co", "Delta", "2026-10-11T09:00:00Z", "C-delta-1"),
		recv("x1", "contractor@gmail.com", "Contractor", "2026-10-12T09:00:00Z", "C-acme-2"),
	}
	ids := make(map[string]int64, len(messages))
	for _, m := range messages {
		ids[m.ID] = answerMessage(t, s, m)
	}
	for _, id := range []string{"a1", "a3", "g1", "b1", "b4", "o1", "d1", "x1"} {
		mailQueryVerdict(t, s, ids[id], scope.Owner, scope.ReasonRecipientMatch)
	}
	for _, id := range []string{"a2", "b2", "b3", "o2"} {
		mailQueryVerdict(t, s, ids[id], scope.Owner, scope.ReasonSentByOwner)
	}
	return ids
}

func TestMailQueryCompaniesResolveDeclaredDomainsAndOwners(t *testing.T) {
	s, _ := open(t, map[string]any{"mail.domains.shared": []any{"gmail.com"}})
	seedCompanyMailbox(t, s)
	res, err := s.ExecuteMailQuery(t.Context(), companyPlan(), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != mailquery.KindCompanies || len(res.Rows) != 3 || res.Total != 8 {
		t.Fatalf("rows %d total %d kind %s: %+v", len(res.Rows), res.Total, res.Kind, res.Rows)
	}
	beta, atelierNord, delta := res.Rows[0], res.Rows[1], res.Rows[2]
	if beta.Key != "c:beta inc" || beta.Label != "Beta Inc" || beta.Address != "beta.test" ||
		beta.Messages != 4 || beta.Sent != 2 || beta.Received != 2 || beta.Threads != 4 ||
		beta.Contacts != 3 || beta.LastAt != "2026-10-08T09:00:00Z" {
		t.Fatalf("Beta Inc row %+v", beta)
	}
	if atelierNord.Key != "c:atelier nord" || atelierNord.Label != "Atelier Nord" || atelierNord.Address != "atelier-nord.test" ||
		atelierNord.Messages != 3 || atelierNord.Sent != 1 || atelierNord.Received != 2 || atelierNord.Threads != 3 ||
		atelierNord.Contacts != 2 || atelierNord.LastAt != "2026-10-03T09:00:00Z" {
		t.Fatalf("Atelier Nord row %+v", atelierNord)
	}
	if delta.Key != "d:delta.co" || delta.Label != "Delta" || delta.Address != "delta.co" ||
		delta.Messages != 1 || delta.Sent != 0 || delta.Received != 1 || delta.Contacts != 1 {
		t.Fatalf("Delta row %+v", delta)
	}
	for _, row := range res.Rows {
		if row.Address == "gmail.com" || row.Label == "gmail.com" || row.Address == "acme.test" ||
			row.Label == "Acme" || strings.Contains(row.Key, "acme.test") {
			t.Fatalf("a personal or owner organization leaked: %+v", row)
		}
	}
}

// TestMailQueryCompaniesExcludeSharedStoreContent guards the owner-facing
// content boundary of the counterpart analysis. A picked shared store can
// mark its own mailbox owner's mail verdict=owner; it must never rank as an
// organization or enter a company profile.
func TestMailQueryCompaniesExcludeSharedStoreContent(t *testing.T) {
	s, _ := open(t, map[string]any{
		"mail.domains.shared":  []any{"gmail.com"},
		source.SettingSelected: []any{source.StoreKey("src", "store-1"), source.StoreKey("src", "store-2")},
	})
	seedCompanyMailbox(t, s)
	if _, err := s.UpsertFolder(t.Context(), mail.Folder{SourceID: "src", StoreID: "store-2", StoreName: "Shared",
		Path: "Inbox", Role: mail.RoleInbox, StoreKind: mail.StoreShared}); err != nil {
		t.Fatal(err)
	}
	hidden := mailtest.Message("src", "shared-secret")
	hidden.StoreID, hidden.FolderPath = "store-2", "Inbox"
	hidden.SenderName, hidden.SenderAddress = "Hidden", "hidden@secret.example"
	hidden.ReceivedAt, hidden.SentOn = "2026-10-20T09:00:00Z", "2026-10-20T09:00:00Z"
	hidden.Subject = "Shared content must not rank"
	hiddenID := answerMessage(t, s, hidden)
	mailQueryVerdict(t, s, hiddenID, scope.Owner, scope.ReasonRecipientMatch)

	res, err := s.ExecuteMailQuery(t.Context(), companyPlan(), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 8 {
		t.Fatalf("shared content changed the company total: %+v", res)
	}
	for _, row := range res.Rows {
		if row.Key == "d:secret.example" || row.Label == "secret.example" {
			t.Fatalf("shared content ranked as an organization: %+v", row)
		}
	}
	profile, err := s.CompanyProfile(t.Context(), "d:secret.example", time.Time{}, time.Time{}, 0, 30,
		worldTime("2026-10-26T12:00:00Z"))
	if !errors.Is(err, ErrUnknownCompany) {
		t.Fatalf("shared-only domain profile = %+v, %v", profile, err)
	}
}

func TestMailQueryCompaniesMetricDirectionAndContactFilter(t *testing.T) {
	s, _ := open(t, map[string]any{"mail.domains.shared": []any{"gmail.com"}})
	seedCompanyMailbox(t, s)
	cases := []struct {
		name   string
		edit   func(*mailquery.Plan)
		keys   []string
		counts []int
		total  int
	}{
		{"exchanges", func(p *mailquery.Plan) { p.Metric = mailquery.MetricExchanges },
			[]string{"c:beta inc", "c:atelier nord"}, []int{4, 3}, 7},
		{"threads", func(p *mailquery.Plan) { p.Metric = mailquery.MetricThreads },
			[]string{"c:beta inc", "c:atelier nord", "d:delta.co"}, []int{4, 3, 1}, 8},
		{"sent", func(p *mailquery.Plan) { p.Direction = mailquery.DirectionSent },
			[]string{"c:beta inc", "c:atelier nord"}, []int{2, 1}, 3},
		{"received", func(p *mailquery.Plan) { p.Direction = mailquery.DirectionReceived },
			[]string{"c:beta inc", "c:atelier nord", "d:delta.co"}, []int{2, 2, 1}, 5},
		{"ascending", func(p *mailquery.Plan) { p.Order = mailquery.OrderAsc },
			[]string{"d:delta.co", "c:atelier nord", "c:beta inc"}, []int{1, 3, 4}, 8},
		{"limit", func(p *mailquery.Plan) { p.Limit = 1 },
			[]string{"c:beta inc"}, []int{4}, 8},
		{"label", func(p *mailquery.Plan) { p.Contact = "beta inc" }, []string{"c:beta inc"}, []int{4}, 4},
		{"domain", func(p *mailquery.Plan) { p.Contact = "gamma.dev" }, []string{"c:beta inc"}, []int{4}, 4},
		{"address", func(p *mailquery.Plan) { p.Contact = "carol@" }, []string{"c:beta inc"}, []int{4}, 4},
		{"owner", func(p *mailquery.Plan) { p.Contact = "acme" }, nil, nil, 0},
		{"personal", func(p *mailquery.Plan) { p.Contact = "personal@gmail.com" }, nil, nil, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := companyPlan()
			c.edit(&p)
			res, err := s.ExecuteMailQuery(t.Context(), p, time.UTC)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Rows) != len(c.keys) {
				t.Fatalf("rows %+v, want %v", res.Rows, c.keys)
			}
			for i, key := range c.keys {
				if res.Rows[i].Key != key || res.Rows[i].Messages != c.counts[i] {
					t.Fatalf("row %d = %+v, want %s/%d", i, res.Rows[i], key, c.counts[i])
				}
			}
			if res.Total != c.total {
				t.Fatalf("total %d, want %d", res.Total, c.total)
			}
		})
	}
}

func TestMailQueryCompaniesDeclaredOnSharedDomainHasNoAddress(t *testing.T) {
	s, _ := open(t, map[string]any{"mail.domains.shared": []any{"gmail.com"}})
	ref := StoreRef{SourceID: "src", StoreID: "store-1"}
	if err := s.ReplaceContacts(t.Context(), ref, "Contacts", []contacts.Contact{
		{EntryID: "solo", DisplayName: "Solo", Company: "Solo LLC", Emails: []string{"solo@gmail.com"}},
	}); err != nil {
		t.Fatal(err)
	}
	m := mailtest.Message("src", "solo-1")
	m.SenderName, m.SenderAddress = "Solo", "solo@gmail.com"
	m.ReceivedAt, m.SentOn = "2026-10-05T09:00:00Z", "2026-10-05T09:00:00Z"
	m.ConversationID = "C-solo"
	id := answerMessage(t, s, m)
	mailQueryVerdict(t, s, id, scope.Owner, scope.ReasonRecipientMatch)
	res, err := s.ExecuteMailQuery(t.Context(), companyPlan(), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Total != 1 {
		t.Fatalf("rows %+v total %d", res.Rows, res.Total)
	}
	row := res.Rows[0]
	if row.Key != "c:solo llc" || row.Label != "Solo LLC" || row.Address != "" ||
		row.Messages != 1 || row.Received != 1 || row.Contacts != 1 {
		t.Fatalf("declared company on a shared domain: %+v", row)
	}
}

func TestMailQueryCompaniesCountsOneMessagePerOrganization(t *testing.T) {
	s, _ := open(t, nil)
	ref := StoreRef{SourceID: "src", StoreID: "store-1"}
	if err := s.ReplaceContacts(t.Context(), ref, "Contacts", []contacts.Contact{
		{EntryID: "alice", DisplayName: "Alice", Company: "Atelier Nord", Emails: []string{"alice@atelier-nord.test"}},
	}); err != nil {
		t.Fatal(err)
	}
	id := answerMessage(t, s, sent("one", "2026-10-02T09:00:00Z",
		mail.Recipient{Kind: mail.To, Name: "Alice", Address: "alice@atelier-nord.test"},
		mail.Recipient{Kind: mail.To, Name: "Bob", Address: "bob@atelier-nord.test"}))
	mailQueryVerdict(t, s, id, scope.Owner, scope.ReasonSentByOwner)
	res, err := s.ExecuteMailQuery(t.Context(), companyPlan(), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Total != 1 {
		t.Fatalf("one sent message to two people at one company: rows %+v total %d", res.Rows, res.Total)
	}
	row := res.Rows[0]
	if row.Key != "c:atelier nord" || row.Label != "Atelier Nord" || row.Address != "atelier-nord.test" ||
		row.Messages != 1 || row.Sent != 1 || row.Received != 0 || row.Threads != 1 || row.Contacts != 2 {
		t.Fatalf("one message must count once per organization: %+v", row)
	}
}

func TestMailQueryExactContactAddressesOverrideSubstring(t *testing.T) {
	s, _ := open(t, map[string]any{"mail.domains.shared": []any{"gmail.com"}})
	seedCompanyMailbox(t, s)
	plan := companyPlan()
	plan.Contact = "Atelier Nord"
	plan.ContactAddresses = []string{"carol@beta.test"}
	res, err := s.ExecuteMailQuery(t.Context(), plan.Normalize(), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Rows[0].Key != "c:beta inc" || res.Rows[0].Messages != 2 || res.Total != 2 {
		t.Fatalf("exact address scope on companies: rows %+v total %d", res.Rows, res.Total)
	}

	contacts := contactPlan()
	contacts.Contact = "Atelier Nord"
	contacts.ContactAddresses = []string{"carol@beta.test", "CAROL@BETA.IO"}
	contactRes, err := s.ExecuteMailQuery(t.Context(), contacts.Normalize(), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(contactRes.Rows) != 1 || contactRes.Rows[0].Address != "carol@beta.test" ||
		contactRes.Rows[0].Messages != 2 || contactRes.Total != 2 {
		t.Fatalf("exact address scope on contacts: rows %+v total %d", contactRes.Rows, contactRes.Total)
	}

	messages := mailquery.Plan{Kind: mailquery.KindMessages, Action: mailquery.ActionList,
		Direction: mailquery.DirectionBoth, TimeRange: mailquery.TimeAll, Limit: 10, Order: mailquery.OrderDesc,
		Contact: "Atelier Nord", ContactAddresses: []string{"carol@beta.test"}}
	messageRes, err := s.ExecuteMailQuery(t.Context(), messages.Normalize(), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if messageRes.Total != 2 || len(messageRes.Rows) != 2 {
		t.Fatalf("exact address scope on messages: rows %+v total %d", messageRes.Rows, messageRes.Total)
	}
	for _, row := range messageRes.Rows {
		if row.Subject != "Sent b2" && row.Subject != "Sent b3" {
			t.Fatalf("substring contact leaked another message: %+v", row)
		}
	}
}

func TestMailQueryContactFilterFoldsAndKeepsWildcardsLiteral(t *testing.T) {
	s, _ := open(t, nil)
	if err := s.SaveReported(t.Context(), StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	recv := func(id, from, name, when string) mail.Message {
		m := mailtest.Message("src", id)
		m.SenderName, m.SenderAddress = name, from
		m.ReceivedAt, m.SentOn = when, when
		m.Subject = "Received " + id
		m.ConversationID = "C-" + id
		return m
	}
	ids := seedMsgs(t, s,
		recv("e1", "ÉMILE.DUPONT@EXEMPLE.CA", "Émile Dupont", "2026-10-20T09:00:00Z"),
		recv("p1", "percent@example.com", "100% Service", "2026-10-21T09:00:00Z"),
		recv("u1", "underscore@example.com", "under_score", "2026-10-22T09:00:00Z"),
	)
	for _, id := range ids {
		mailQueryVerdict(t, s, id, scope.Owner, scope.ReasonRecipientMatch)
	}
	contact := func(term string) mailquery.Result {
		t.Helper()
		p := contactPlan()
		p.Contact = term
		res, err := s.ExecuteMailQuery(t.Context(), p.Normalize(), time.UTC)
		if err != nil {
			t.Fatalf("contacts %q: %v", term, err)
		}
		return res
	}
	if res := contact("emile"); len(res.Rows) != 1 || res.Rows[0].Key != "émile.dupont@exemple.ca" || res.Rows[0].Label != "Émile Dupont" {
		t.Fatalf("accented contact: %+v", res.Rows)
	}
	if res := contact("100%"); len(res.Rows) != 1 || res.Rows[0].Address != "percent@example.com" {
		t.Fatalf("literal percent contact: %+v", res.Rows)
	}
	if res := contact("%"); len(res.Rows) != 1 || res.Rows[0].Address != "percent@example.com" {
		t.Fatalf("a lone percent must stay literal: %+v", res.Rows)
	}
	if res := contact("under_score"); len(res.Rows) != 1 || res.Rows[0].Address != "underscore@example.com" {
		t.Fatalf("literal underscore contact: %+v", res.Rows)
	}
	messages := func(term string) mailquery.Result {
		t.Helper()
		p := mailquery.Plan{Kind: mailquery.KindMessages, Action: mailquery.ActionList,
			Direction: mailquery.DirectionBoth, TimeRange: mailquery.TimeAll, Limit: 10, Order: mailquery.OrderDesc,
			Contact: term}
		res, err := s.ExecuteMailQuery(t.Context(), p.Normalize(), time.UTC)
		if err != nil {
			t.Fatalf("messages %q: %v", term, err)
		}
		return res
	}
	if res := messages("emile"); res.Total != 1 || len(res.Rows) != 1 || res.Rows[0].Subject != "Received e1" {
		t.Fatalf("accented message contact: %+v", res.Rows)
	}
	if res := messages("%"); res.Total != 1 || len(res.Rows) != 1 || res.Rows[0].Subject != "Received p1" {
		t.Fatalf("a lone percent must not select every message: %+v", res.Rows)
	}
}

func TestMailQueryMessagesCountListAndOwnerOnly(t *testing.T) {
	s, _ := open(t, nil)
	seedContactMailbox(t, s)
	seedSharedStoreMessage(t, s)
	seedOwnerDraft(t, s)
	count := func(p mailquery.Plan) int {
		t.Helper()
		res, err := s.ExecuteMailQuery(t.Context(), p.Normalize(), time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		return res.Total
	}
	both := mailquery.Plan{Kind: mailquery.KindMessages, Action: mailquery.ActionCount,
		Direction: mailquery.DirectionBoth, TimeRange: mailquery.TimeAll, Limit: 10, Order: mailquery.OrderDesc}
	if got := count(both); got != 7 {
		t.Fatalf("both count %d, want 7 (the owner-addressed sent copy is still one message)", got)
	}
	both.Direction = mailquery.DirectionReceived
	if got := count(both); got != 3 {
		t.Fatalf("received count %d, want 3", got)
	}
	both.Direction = mailquery.DirectionSent
	if got := count(both); got != 4 {
		t.Fatalf("sent count %d, want 4", got)
	}
	both.Direction = mailquery.DirectionBoth
	both.Contact = "ada"
	if got := count(both); got != 4 {
		t.Fatalf("Ada count %d, want 4", got)
	}
	// "The latest message from Ada" is the newest message Ada sent, never
	// the owner's later reply. Received-only is the direction the planner
	// contract requires for a single latest-from question.
	latestFrom := mailquery.Plan{Kind: mailquery.KindMessages, Action: mailquery.ActionList,
		Direction: mailquery.DirectionReceived, Contact: "ada",
		TimeRange: mailquery.TimeAll, Limit: 1, Order: mailquery.OrderDesc}
	latest, err := s.ExecuteMailQuery(t.Context(), latestFrom.Normalize(), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(latest.Rows) != 1 || latest.Rows[0].Subject != "Received a2" ||
		latest.Rows[0].SenderName != "Ada Lovelace" || latest.Rows[0].SentFlag {
		t.Fatalf("latest received from Ada = %+v, want Received a2 sent by Ada", latest.Rows)
	}
	both.Contact = ""
	both.Since, both.Until = "2026-10-23T00:00:00Z", "2026-10-25T00:00:00Z"
	if got := count(both); got != 2 {
		t.Fatalf("bounded count %d, want 2", got)
	}

	list := mailquery.Plan{Kind: mailquery.KindMessages, Action: mailquery.ActionList,
		Direction: mailquery.DirectionReceived, TimeRange: mailquery.TimeAll, Limit: 2, Order: mailquery.OrderDesc}
	res, err := s.ExecuteMailQuery(t.Context(), list, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 3 || len(res.Rows) != 2 {
		t.Fatalf("list total %d rows %d", res.Total, len(res.Rows))
	}
	if res.Rows[0].Subject != "Received g1" || res.Rows[1].Subject != "Received a2" {
		t.Fatalf("list order %+v", res.Rows)
	}
	if res.Rows[0].MessageID <= 0 || res.Rows[0].SenderName != "Grace Hopper" || res.Rows[0].SentFlag {
		t.Fatalf("list row %+v", res.Rows[0])
	}
	asc := list
	asc.Order, asc.Limit = mailquery.OrderAsc, 1
	res, err = s.ExecuteMailQuery(t.Context(), asc, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Rows[0].Subject != "Received a1" {
		t.Fatalf("ascending latest list %+v, want the oldest received message", res.Rows)
	}
}

func TestMailQueryTimeBucketsAndEmptyMailbox(t *testing.T) {
	s, _ := open(t, nil)
	seedContactMailbox(t, s)
	seedSharedStoreMessage(t, s)
	seedOwnerDraft(t, s)
	month := mailquery.Plan{Kind: mailquery.KindTime, Action: mailquery.ActionList,
		Direction: mailquery.DirectionBoth, GroupBy: mailquery.GroupMonth, TimeRange: mailquery.TimeAll,
		Limit: 12, Order: mailquery.OrderDesc}
	res, err := s.ExecuteMailQuery(t.Context(), month, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 7 || len(res.Rows) != 1 || res.Rows[0].Key != "2026-10" || res.Rows[0].Messages != 7 {
		t.Fatalf("month rows %+v total %d", res.Rows, res.Total)
	}
	weekday := month
	weekday.GroupBy = mailquery.GroupWeekday
	res, err = s.ExecuteMailQuery(t.Context(), weekday, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 7 || len(res.Rows) == 0 {
		t.Fatalf("weekday rows %+v total %d", res.Rows, res.Total)
	}
	for _, row := range res.Rows {
		if row.Key == "" || row.Messages == 0 {
			t.Fatalf("weekday row %+v", row)
		}
	}

	empty, _ := open(t, nil)
	res, err = empty.ExecuteMailQuery(t.Context(), contactPlan(), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rows == nil || len(res.Rows) != 0 || res.Total != 0 {
		t.Fatalf("empty mailbox: %+v", res)
	}
}

func TestMailQueryRejectsUnresolvedRangeAndUnvalidatedPlan(t *testing.T) {
	s, _ := open(t, nil)
	plan := contactPlan()
	plan.TimeRange = mailquery.TimeLast7Days
	if _, err := s.ExecuteMailQuery(t.Context(), plan, time.UTC); err == nil {
		t.Fatal("an unresolved relative range must be rejected, not silently read as all time")
	}
	plan = contactPlan()
	plan.Limit = 500
	if _, err := s.ExecuteMailQuery(t.Context(), plan, time.UTC); err == nil {
		t.Fatal("an invalid limit must be rejected at the store boundary")
	}
}

// A recipient row written before address_norm existed (or left empty) must
// still count by its raw address. The old COALESCE treated a NOT NULL column
// as if it could be NULL, so the empty string silently dropped the message.
func TestContactsCountARecipientWithAnEmptyNormalizedAddress(t *testing.T) {
	s, _ := open(t, map[string]any{"mail.domains.shared": []any{"gmail.com"}})
	ids := seedCompanyMailbox(t, s)
	if _, err := s.db.ExecContext(t.Context(),
		`UPDATE recipients SET address_norm = '' WHERE message_id = ? AND lower(trim(address)) = 'carol@beta.test'`,
		ids["b2"]); err != nil {
		t.Fatal(err)
	}
	plan := contactPlan()
	plan.Contact = "Carol"
	plan.ContactAddresses = []string{"carol@beta.test"}
	res, err := s.ExecuteMailQuery(t.Context(), plan.Normalize(), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Rows[0].Address != "carol@beta.test" || res.Rows[0].Messages != 2 {
		t.Fatalf("an empty address_norm dropped the recipient: %+v total %d", res.Rows, res.Total)
	}
}

// TestMailQueryMessageAndTimeMatchOnePopulation: a message count and a time
// bucket are two projections of the same population. They used to build the
// WHERE clause separately, so a filter added to one path (a direction, a
// contact, a bound) could silently change only that path. The table runs the
// same plan through both kinds and requires the counts to agree.
func TestMailQueryMessageAndTimeMatchOnePopulation(t *testing.T) {
	s, _ := open(t, nil)
	seedContactMailbox(t, s)
	seedSharedStoreMessage(t, s)
	seedOwnerDraft(t, s)

	base := mailquery.Plan{Kind: mailquery.KindMessages, Action: mailquery.ActionCount,
		Direction: mailquery.DirectionBoth, TimeRange: mailquery.TimeAll, Limit: 10, Order: mailquery.OrderDesc}
	plans := map[string]mailquery.Plan{
		"both directions":    base,
		"received only":      withDirection(base, mailquery.DirectionReceived),
		"sent only":          withDirection(base, mailquery.DirectionSent),
		"substring contact":  withContact(base, "ada"),
		"sent and contact":   withContact(withDirection(base, mailquery.DirectionSent), "ada"),
		"exact counterpart":  withAddresses(base, "ada@example.org"),
		"exact and received": withAddresses(withDirection(base, mailquery.DirectionReceived), "ada@example.org"),
		"bounded":            withRange(base, "2026-10-23T00:00:00Z", "2026-10-25T00:00:00Z"),
		"bounded contact":    withRange(withContact(base, "ada"), "2026-10-22T00:00:00Z", "2026-10-26T00:00:00Z"),
	}
	for name, plan := range plans {
		t.Run(name, func(t *testing.T) {
			messagePlan := plan.Normalize()
			res, err := s.ExecuteMailQuery(t.Context(), messagePlan, time.UTC)
			if err != nil {
				t.Fatal(err)
			}
			timePlan := messagePlan
			timePlan.Kind, timePlan.Action = mailquery.KindTime, mailquery.ActionList
			timePlan.GroupBy = mailquery.GroupMonth
			buckets, err := s.ExecuteMailQuery(t.Context(), timePlan, time.UTC)
			if err != nil {
				t.Fatal(err)
			}
			if buckets.Total != res.Total {
				t.Fatalf("time total %d differs from message count %d", buckets.Total, res.Total)
			}
			sum := 0
			for _, row := range buckets.Rows {
				sum += row.Messages
			}
			if sum != res.Total {
				t.Fatalf("bucket rows sum to %d, message count is %d", sum, res.Total)
			}
		})
	}
}

func withDirection(p mailquery.Plan, direction mailquery.Direction) mailquery.Plan {
	p.Direction = direction
	return p
}

func withContact(p mailquery.Plan, contact string) mailquery.Plan {
	p.Contact = contact
	return p
}

func withAddresses(p mailquery.Plan, addresses ...string) mailquery.Plan {
	p.ContactAddresses = addresses
	return p
}

func withRange(p mailquery.Plan, since, until string) mailquery.Plan {
	p.Since, p.Until = since, until
	return p
}
