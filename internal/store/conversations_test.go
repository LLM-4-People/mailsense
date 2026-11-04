package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// conversationTestMember seeds one member of a conversation and returns its
// row id. It uses the same owner content boundary as the thread tests.
func conversationTestMember(t *testing.T, s *Store, id, conversationID string, verdict scope.Verdict, mutate func(*mail.Message)) int64 {
	t.Helper()
	m := mailtest.Message("src", id)
	m.ConversationID = conversationID
	m.Subject = "Subject " + id
	m.ReceivedAt, m.SentOn = "2026-11-03T08:00:00Z", "2026-11-03T08:00:00Z"
	if mutate != nil {
		mutate(&m)
	}
	rowID := answerMessage(t, s, m)
	answerVerdict(t, s, rowID, verdict)
	return rowID
}

func digestFor(t *testing.T, set ConversationDigestSet, conversationID string) ConversationDigest {
	t.Helper()
	for _, d := range set.Conversations {
		if d.ConversationID == conversationID {
			return d
		}
	}
	t.Fatalf("no digest for %q in %+v", conversationID, set.Conversations)
	return ConversationDigest{}
}

func TestConversationDigestsOwnerVisibleConversation(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	if err := s.SaveReported(ctx, StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	owner := mail.Recipient{Kind: mail.To, Name: "Owner", Address: mailtest.Owner}

	firstID := conversationTestMember(t, s, "conv-1", "C-thread", scope.Owner, func(m *mail.Message) {
		m.Subject = "First subject"
		m.Body = "First body"
		m.SenderName, m.SenderAddress = "Client", "client@example.test"
		m.SentRepresentingAddress = "client@example.test"
		m.Recipients = []mail.Recipient{owner}
		m.IsRead, m.HasAttachments = true, false
	})
	// A sent item's instant is sent_on, not the delivery-time received_at.
	sentID := conversationTestMember(t, s, "conv-2", "C-thread", scope.Owner, func(m *mail.Message) {
		m.Subject = "Owner reply"
		m.Body = "Owner body"
		m.FolderRole, m.FolderPath = mail.RoleSent, "Sent"
		m.SenderName, m.SenderAddress, m.SentRepresentingAddress = "Amélie Durand", mailtest.Owner, mailtest.Owner
		m.AddressedToMe = mail.AddressedNo
		m.Recipients = []mail.Recipient{
			{Kind: mail.To, Name: "Client", Address: "client@example.test"},
			{Kind: mail.Cc, Name: "Colleague", Address: "colleague@example.test"},
			owner,
		}
		m.SentOn, m.ReceivedAt = "2026-11-03T10:00:00Z", "2026-11-03T07:00:00Z"
		m.IsRead, m.HasAttachments = true, false
	})
	latestID := conversationTestMember(t, s, "conv-3", "C-thread", scope.Owner, func(m *mail.Message) {
		m.Subject = "Réunion Budget"
		m.Body = strings.Repeat("alpha\tbeta\n", 200)
		m.SenderName, m.SenderAddress = "Élodie MARTIN", "Elodie@Example.ORG"
		m.SentRepresentingAddress = "Elodie@Example.ORG"
		m.Recipients = []mail.Recipient{owner}
		m.ReceivedAt, m.SentOn = "2026-11-03T13:00:00Z", "2026-11-03T13:00:00Z"
		m.IsRead, m.HasAttachments = false, true
		m.TaskDueDate, m.FlagStatus = "2026-11-10T00:00:00Z", mail.Int(2)
	})

	set, err := s.ConversationDigests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if set.Conversations == nil {
		t.Fatal("conversation digests must be a non-nil slice")
	}
	if len(set.Conversations) != 1 {
		t.Fatalf("digests %+v", set.Conversations)
	}
	d := set.Conversations[0]
	if d.ConversationID != "C-thread" || d.Subject != "Réunion Budget" ||
		d.SenderName != "Élodie MARTIN" || d.SenderAddress != "Elodie@Example.ORG" {
		t.Fatalf("latest metadata %+v", d)
	}
	wantSearch := mail.Fold("Réunion Budget\nÉlodie MARTIN\nElodie@Example.ORG")
	if d.SearchText != wantSearch {
		t.Fatalf("search text %q, want %q", d.SearchText, wantSearch)
	}
	if !strings.Contains(d.SearchText, "reunion") || !strings.Contains(d.SearchText, "elodie@example.org") {
		t.Fatalf("search text is not folded: %q", d.SearchText)
	}
	if d.LastMessageID != latestID || d.LastAt != "2026-11-03T13:00:00Z" ||
		d.FirstAt != "2026-11-03T08:00:00Z" {
		t.Fatalf("conversation span %+v (first %d, sent %d, latest %d)", d, firstID, sentID, latestID)
	}
	var revision int64
	if err := s.db.QueryRowContext(ctx, "SELECT revision FROM messages WHERE id = ?", latestID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if d.LastRevision != revision {
		t.Fatalf("last revision %d, want %d", d.LastRevision, revision)
	}
	if d.Messages != 3 || d.Sent != 1 || d.Received != 2 || d.Unread != 1 {
		t.Fatalf("counts %+v", d)
	}
	if d.LastSent || d.LastIsRead || !d.LastHasAttachments {
		t.Fatalf("latest flags %+v", d)
	}
	if d.CounterpartyAddress != "Elodie@Example.ORG" {
		t.Fatalf("counterparty address %q, want the latest received sender", d.CounterpartyAddress)
	}
	if d.DueDate != "2026-11-10" {
		t.Fatalf("due date %q", d.DueDate)
	}
	if set.Coverage != (ConversationCoverage{Messages: 3, Conversations: 1, Unthreaded: 0}) {
		t.Fatalf("coverage %+v", set.Coverage)
	}

	snippets, err := s.ConversationSnippets(ctx, []int64{latestID, 1 << 40})
	if err != nil {
		t.Fatal(err)
	}
	snippet, ok := snippets[latestID]
	if !ok || len(snippets) != 1 {
		t.Fatalf("snippets %+v", snippets)
	}
	if utf8.RuneCountInString(snippet) > threadSnippetRunes {
		t.Fatalf("snippet has %d runes: %q", utf8.RuneCountInString(snippet), snippet)
	}
	if strings.ContainsAny(snippet, "\n\t") || strings.Contains(snippet, "  ") {
		t.Fatalf("snippet not collapsed: %q", snippet)
	}
	if utf8.RuneCountInString(snippet) != threadSnippetRunes {
		t.Fatalf("snippet was not bounded to %d runes: %d", threadSnippetRunes, utf8.RuneCountInString(snippet))
	}

	// The digest must agree with the conversation read on the facts both
	// expose, so a thread-first mailbox and a thread page never disagree.
	thread, err := s.ThreadMessages(ctx, "C-thread", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if d.Subject != thread.Subject || d.LastMessageID != thread.NewestMessageID ||
		d.Messages != thread.Count || d.FirstAt != thread.FirstAt || d.LastAt != thread.LastAt {
		t.Fatalf("digest %+v disagrees with thread %+v", d, thread)
	}
}

func TestConversationDigestKeepsCounterpartyWhenOwnerRepliedLast(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	if err := s.SaveReported(ctx, StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	owner := mail.Recipient{Kind: mail.To, Name: "Owner", Address: mailtest.Owner}
	conversationTestMember(t, s, "client-1", "C-sentlast", scope.Owner, func(m *mail.Message) {
		m.SenderName, m.SenderAddress = "Client", "client@client-exemple.test"
		m.SentRepresentingAddress = "client@client-exemple.test"
		m.Recipients = []mail.Recipient{owner}
		m.ReceivedAt, m.SentOn = "2026-11-03T08:00:00Z", "2026-11-03T08:00:00Z"
	})
	sentID := conversationTestMember(t, s, "owner-1", "C-sentlast", scope.Owner, func(m *mail.Message) {
		m.FolderRole, m.FolderPath = mail.RoleSent, "Sent"
		m.SenderName, m.SenderAddress, m.SentRepresentingAddress = "Owner", mailtest.Owner, mailtest.Owner
		m.AddressedToMe = mail.AddressedNo
		m.Recipients = []mail.Recipient{{Kind: mail.To, Name: "Client", Address: "client@client-exemple.test"}}
		m.ReceivedAt, m.SentOn = "2026-11-03T07:00:00Z", "2026-11-03T10:00:00Z"
	})

	set, err := s.ConversationDigests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d := digestFor(t, set, "C-sentlast")
	if d.LastMessageID != sentID || !d.LastSent || d.SenderAddress != mailtest.Owner {
		t.Fatalf("latest sent message %+v", d)
	}
	if d.CounterpartyAddress != "client@client-exemple.test" {
		t.Fatalf("counterparty %q, want client@client-exemple.test", d.CounterpartyAddress)
	}
}

func TestConversationDigestFollowsInstantNotRowID(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	if err := s.SaveReported(ctx, StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	owner := mail.Recipient{Kind: mail.To, Name: "Owner", Address: mailtest.Owner}
	seed := func(id, instant, subject string) int64 {
		return conversationTestMember(t, s, id, "C-order", scope.Owner, func(m *mail.Message) {
			m.Subject = subject
			m.SenderName, m.SenderAddress = "Client", "client@example.test"
			m.Recipients = []mail.Recipient{owner}
			m.ReceivedAt, m.SentOn = instant, instant
		})
	}
	// Newest first, exactly as an initial sync streams it: the newest mail
	// carries the smallest row id.
	newestID := seed("sync-1", "2026-11-04T09:00:00Z", "Newest")
	secondID := seed("sync-2", "2026-11-03T09:00:00Z", "Second")
	oldestID := seed("sync-3", "2026-11-02T09:00:00Z", "Oldest")
	if !(newestID < secondID && secondID < oldestID) {
		t.Fatalf("seed ids are not reverse chronological: %d %d %d", newestID, secondID, oldestID)
	}
	set, err := s.ConversationDigests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d := digestFor(t, set, "C-order")
	if d.LastMessageID != newestID || d.Subject != "Newest" || d.LastAt != "2026-11-04T09:00:00Z" ||
		d.FirstAt != "2026-11-02T09:00:00Z" || d.Messages != 3 {
		t.Fatalf("digest did not follow the mail instant: %+v", d)
	}
}

func TestConversationDigestExcludesNewerHiddenMail(t *testing.T) {
	s, _ := open(t, map[string]any{
		source.SettingSelected: mailtest.Picked([]string{"src"}, [2]string{"src", "store-2"}),
	})
	ctx := t.Context()
	if err := s.SaveReported(ctx, StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertFolder(ctx, mail.Folder{SourceID: "src", StoreID: "store-2", StoreName: "Shared",
		Path: "Inbox", Role: mail.RoleInbox, StoreKind: mail.StoreShared}); err != nil {
		t.Fatal(err)
	}
	owner := mail.Recipient{Kind: mail.To, Name: "Owner", Address: mailtest.Owner}
	visibleID := conversationTestMember(t, s, "visible", "C-filter", scope.Owner, func(m *mail.Message) {
		m.Subject = "Visible subject"
		m.SenderName, m.SenderAddress = "Client", "client@example.test"
		m.Recipients = []mail.Recipient{owner}
		m.ReceivedAt, m.SentOn = "2026-11-03T08:00:00Z", "2026-11-03T08:00:00Z"
	})
	hidden := func(id, subject, instant string, mutate func(*mail.Message)) {
		t.Helper()
		conversationTestMember(t, s, id, "C-filter", scope.Owner, func(m *mail.Message) {
			m.Subject = subject
			m.SenderName, m.SenderAddress = "Hidden", "hidden@example.test"
			m.Recipients = []mail.Recipient{owner}
			m.ReceivedAt, m.SentOn = instant, instant
			if mutate != nil {
				mutate(m)
			}
		})
	}
	hidden("draft", "Draft must not win", "2026-11-03T15:00:00Z", func(m *mail.Message) {
		m.FolderRole, m.FolderPath = mail.RoleDrafts, "Drafts"
	})
	hidden("deleted", "Deleted subject", "2026-11-03T16:00:00Z", func(m *mail.Message) {
		m.FolderRole, m.FolderPath = mail.RoleDeleted, "Deleted Items"
	})
	hidden("junk", "Junk subject", "2026-11-03T18:00:00Z", func(m *mail.Message) {
		m.FolderRole, m.FolderPath = mail.RoleJunk, "Junk Email"
	})
	hidden("shared", "Shared subject", "2026-11-03T17:00:00Z", func(m *mail.Message) {
		m.StoreID, m.FolderPath = "store-2", "Inbox"
	})
	hidden("unpicked", "Unpicked subject", "2026-11-03T19:00:00Z", func(m *mail.Message) {
		m.StoreID = "store-3"
	})

	set, err := s.ConversationDigests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Conversations) != 1 {
		t.Fatalf("hidden mail produced digests: %+v", set.Conversations)
	}
	d := set.Conversations[0]
	if d.LastMessageID != visibleID || d.Subject != "Visible subject" || d.Messages != 1 ||
		d.LastAt != "2026-11-03T08:00:00Z" {
		t.Fatalf("hidden mail won or counted: %+v", d)
	}
	if set.Coverage != (ConversationCoverage{Messages: 1, Conversations: 1, Unthreaded: 0}) {
		t.Fatalf("coverage counted hidden mail: %+v", set.Coverage)
	}
}

func TestConversationDigestDueDateOnlyWhileOpen(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	openID := conversationTestMember(t, s, "due-open", "C-open", scope.Owner, func(m *mail.Message) {
		m.TaskDueDate, m.FlagStatus = "2026-11-10T00:00:00Z", mail.Int(2)
	})
	nullFlagID := conversationTestMember(t, s, "due-null", "C-null", scope.Owner, func(m *mail.Message) {
		m.TaskDueDate, m.FlagStatus = "2026-11-12T00:00:00Z", mail.OptInt{}
	})
	doneID := conversationTestMember(t, s, "due-done", "C-done", scope.Owner, func(m *mail.Message) {
		m.TaskDueDate, m.FlagStatus = "2026-11-11T00:00:00Z", mail.Int(1)
	})
	noDateID := conversationTestMember(t, s, "due-none", "C-none", scope.Owner, func(m *mail.Message) {
		m.TaskDueDate, m.FlagStatus = "", mail.OptInt{}
	})
	set, err := s.ConversationDigests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		conversationID string
		messageID      int64
		want           string
	}{
		{"C-open", openID, "2026-11-10"},
		{"C-null", nullFlagID, "2026-11-12"},
		{"C-done", doneID, ""},
		{"C-none", noDateID, ""},
	}
	for _, tc := range cases {
		d := digestFor(t, set, tc.conversationID)
		if d.LastMessageID != tc.messageID || d.DueDate != tc.want {
			t.Errorf("%s: due date %q, want %q (%+v)", tc.conversationID, d.DueDate, tc.want, d)
		}
	}
}

func TestConversationParticipantsBatch(t *testing.T) {
	s, _ := open(t, map[string]any{
		source.SettingSelected: mailtest.Picked([]string{"src"}, [2]string{"src", "store-2"}),
	})
	ctx := t.Context()
	if err := s.SaveReported(ctx, StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertFolder(ctx, mail.Folder{SourceID: "src", StoreID: "store-2", StoreName: "Shared",
		Path: "Inbox", Role: mail.RoleInbox, StoreKind: mail.StoreShared}); err != nil {
		t.Fatal(err)
	}
	owner := mail.Recipient{Kind: mail.To, Name: "Owner", Address: mailtest.Owner}
	conversationTestMember(t, s, "p1", "C1", scope.Owner, func(m *mail.Message) {
		m.SenderName, m.SenderAddress = "Client", "client@atelier-nord.test"
		m.Recipients = []mail.Recipient{owner, {Kind: mail.Cc, Name: "Colleague", Address: "colleague@atelier-nord.test"}}
		m.ReceivedAt, m.SentOn = "2026-11-03T08:00:00Z", "2026-11-03T08:00:00Z"
	})
	conversationTestMember(t, s, "p2", "C1", scope.Owner, func(m *mail.Message) {
		m.FolderRole, m.FolderPath = mail.RoleSent, "Sent"
		m.SenderName, m.SenderAddress = "Owner", mailtest.Owner
		m.Recipients = []mail.Recipient{
			{Kind: mail.To, Name: "Client", Address: "client@atelier-nord.test"},
			{Kind: mail.Cc, Name: "Client duplicate", Address: "Client@Atelier-Nord.test"},
			{Kind: mail.Cc, Name: "Colleague", Address: "colleague@atelier-nord.test"},
			{Kind: mail.Bcc, Name: "Hidden", Address: "hidden@atelier-nord.test"},
			owner,
		}
		m.SentOn, m.ReceivedAt = "2026-11-03T10:00:00Z", "2026-11-03T07:00:00Z"
	})
	conversationTestMember(t, s, "p3", "C1", scope.Owner, func(m *mail.Message) {
		m.SenderName, m.SenderAddress = "Second", "second@example.org"
		m.Recipients = []mail.Recipient{owner}
		m.ReceivedAt, m.SentOn = "2026-11-03T09:00:00Z", "2026-11-03T09:00:00Z"
	})
	conversationTestMember(t, s, "p4", "C2", scope.Owner, func(m *mail.Message) {
		m.SenderName, m.SenderAddress = "Third", "third@example.org"
		m.Recipients = []mail.Recipient{owner}
		m.ReceivedAt, m.SentOn = "2026-11-03T11:00:00Z", "2026-11-03T11:00:00Z"
	})
	// A shared store can carry owner verdicts; its participants never enter
	// the owner-facing batch.
	conversationTestMember(t, s, "p5", "C-shared", scope.Owner, func(m *mail.Message) {
		m.StoreID, m.FolderPath = "store-2", "Inbox"
		m.SenderName, m.SenderAddress = "Intruder", "intruder@shared.test"
	})
	// A conversation with only owner-sent mail has no external participant,
	// but its participant slice is still an empty array, not nil.
	conversationTestMember(t, s, "p6", "C-owner", scope.Owner, func(m *mail.Message) {
		m.SenderName, m.SenderAddress = "Owner", mailtest.Owner
		m.Recipients = nil
	})

	got, err := s.ConversationParticipants(ctx, []string{"C1", "C2", "C-shared", "C-owner"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	c1, ok := got["C1"]
	if !ok {
		t.Fatalf("no C1 participants: %+v", got)
	}
	if c1.Count != 3 || len(c1.Participants) != 2 {
		t.Fatalf("C1 participants %+v", c1)
	}
	if p := c1.Participants[0]; p.Address != "client@atelier-nord.test" || p.Name != "Client" || p.Messages != 2 ||
		p.LastAt != "2026-11-03T10:00:00Z" {
		t.Fatalf("client participant %+v", p)
	}
	if p := c1.Participants[1]; p.Address != "colleague@atelier-nord.test" || p.Messages != 1 ||
		p.LastAt != "2026-11-03T10:00:00Z" {
		t.Fatalf("colleague participant %+v", p)
	}
	for _, p := range c1.Participants {
		if p.Address == mailtest.Owner || p.Address == "hidden@atelier-nord.test" {
			t.Fatalf("owner or bcc leaked into participants: %+v", c1.Participants)
		}
	}
	if c2 := got["C2"]; c2.Count != 1 || len(c2.Participants) != 1 || c2.Participants[0].Address != "third@example.org" {
		t.Fatalf("C2 participants %+v", c2)
	}
	if shared, ok := got["C-shared"]; ok && shared.Count != 0 {
		t.Fatalf("shared conversation participants leaked: %+v", shared)
	}
	if ownerOnly, ok := got["C-owner"]; !ok || ownerOnly.Count != 0 || ownerOnly.Participants == nil ||
		len(ownerOnly.Participants) != 0 {
		t.Fatalf("owner-only conversation participants %+v/%v", ownerOnly, ok)
	}
	if _, err := s.ConversationParticipants(ctx, []string{"C1"}, 0); err == nil {
		t.Fatal("a zero participant cap must fail")
	}
}

func TestConversationSnippetsBoundedAndCollapsed(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	id := conversationTestMember(t, s, "snippet", "C-snippet", scope.Owner, func(m *mail.Message) {
		m.Body = "  one\ttwo\nthree   four " + strings.Repeat("word ", 300)
	})
	got, err := s.ConversationSnippets(ctx, []int64{id, 1 << 40})
	if err != nil {
		t.Fatal(err)
	}
	snippet, ok := got[id]
	if !ok || len(got) != 1 {
		t.Fatalf("snippets %+v", got)
	}
	if utf8.RuneCountInString(snippet) > threadSnippetRunes {
		t.Fatalf("%d runes: %q", utf8.RuneCountInString(snippet), snippet)
	}
	if !strings.HasPrefix(snippet, "one two three four word") {
		t.Fatalf("snippet not collapsed from the start: %q", snippet)
	}
	if strings.ContainsAny(snippet, "\n\t") || strings.Contains(snippet, "  ") {
		t.Fatalf("snippet not collapsed: %q", snippet)
	}
}

func TestConversationCoverageUnthreaded(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	threadedID := conversationTestMember(t, s, "threaded", "C-threaded", scope.Owner, nil)
	unthreadedID := conversationTestMember(t, s, "unthreaded", "", scope.Owner, func(m *mail.Message) {
		m.Subject = "Unthreaded"
		m.ReceivedAt, m.SentOn = "2026-11-03T12:00:00Z", "2026-11-03T12:00:00Z"
	})
	set, err := s.ConversationDigests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Conversations) != 1 || set.Conversations[0].LastMessageID != threadedID {
		t.Fatalf("unthreaded mail was listed: %+v", set.Conversations)
	}
	if set.Coverage != (ConversationCoverage{Messages: 2, Conversations: 1, Unthreaded: 1}) {
		t.Fatalf("coverage %+v (threaded %d, unthreaded %d)", set.Coverage, threadedID, unthreadedID)
	}
}

func TestConversationDigestsEmpty(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	set, err := s.ConversationDigests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if set.Conversations == nil || len(set.Conversations) != 0 {
		t.Fatalf("conversations %+v", set.Conversations)
	}
	if set.Coverage != (ConversationCoverage{}) {
		t.Fatalf("coverage %+v", set.Coverage)
	}
	participants, err := s.ConversationParticipants(ctx, nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	if participants == nil || len(participants) != 0 {
		t.Fatalf("participants %+v", participants)
	}
	snippets, err := s.ConversationSnippets(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if snippets == nil || len(snippets) != 0 {
		t.Fatalf("snippets %+v", snippets)
	}
}

func TestCompanyBookResolveExternal(t *testing.T) {
	s, _ := open(t, map[string]any{"mail.domains.shared": []any{"gmail.com"}})
	seedCompanyDirectory(t, s)
	book, err := s.CompanyBook(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// An external declared company resolves.
	org, ok := book.ResolveExternal("alice@atelier-nord.test")
	if !ok || org.Key != "c:atelier nord" || org.Label != "Atelier Nord" || !org.Declared {
		t.Fatalf("external declared organization %+v/%v", org, ok)
	}
	// A bare external domain resolves as a domain organization.
	if org, ok := book.ResolveExternal("dave@delta.co"); !ok || org.Key != "d:delta.co" || org.Declared {
		t.Fatalf("external domain organization %+v/%v", org, ok)
	}
	// Owner domains and owner companies are internal, never external.
	for _, address := range []string{"eve@acme.test", "someone@acme.test", "contractor@gmail.com"} {
		if org, ok := book.ResolveExternal(address); ok {
			t.Errorf("internal address %q resolved external: %+v", address, org)
		}
		if _, ok := book.Resolve(address); !ok {
			t.Errorf("internal address %q must still resolve for IsInternal callers", address)
		}
	}
	// A shared domain without a declaration has no organization at all.
	if org, ok := book.ResolveExternal("personal@gmail.com"); ok {
		t.Fatalf("a shared-domain personal address resolved: %+v", org)
	}
}

// conversationDigestLegacyQuery is the previous whole-mailbox digest read,
// kept only as the parity oracle of the index-ordered stream. It is the exact
// statement the production read replaced: materialized content, three
// DISTINCT aggregates, two window functions and a final sort.
func conversationDigestLegacyQuery(where string) string {
	return `WITH content AS MATERIALIZED (
			SELECT m.id, m.conversation_id, m.subject, m.sender_name, m.sender_address,
				m.revision, m.is_read, m.has_attachments, m.task_due_date, m.flag_status,
				f.role, m.instant AS instant
			FROM messages m
			JOIN folders f ON f.id = m.folder_id
			JOIN scope_verdicts v ON v.message_id = m.id
			WHERE ` + where + ` AND m.conversation_id <> ''
		), agg AS (
			SELECT conversation_id,
				COUNT(DISTINCT id) AS messages,
				COUNT(DISTINCT CASE WHEN role = 'sent' THEN id END) AS sent,
				COUNT(DISTINCT CASE WHEN role <> 'sent' AND is_read = 0 THEN id END) AS unread,
				MIN(NULLIF(instant, '')) AS first_at
			FROM content
			GROUP BY conversation_id
		), ranked AS (
			SELECT content.*,
				ROW_NUMBER() OVER (PARTITION BY conversation_id
					ORDER BY instant DESC, id DESC) AS rn,
				FIRST_VALUE(CASE WHEN role <> 'sent' THEN sender_address END) OVER (
					PARTITION BY conversation_id
					ORDER BY CASE WHEN role <> 'sent' THEN 0 ELSE 1 END,
						instant DESC, id DESC
				) AS latest_received
			FROM content
		)
		SELECT r.conversation_id, r.subject, r.sender_name, r.sender_address, r.id, r.revision,
			r.instant, a.first_at, a.messages, a.sent, a.unread, r.role, r.is_read, r.has_attachments,
			CASE WHEN COALESCE(r.flag_status, 0) <> 1 AND r.task_due_date <> ''
				THEN substr(r.task_due_date, 1, 10) ELSE '' END,
			COALESCE(r.latest_received, '')
		FROM ranked r
		JOIN agg a ON a.conversation_id = r.conversation_id
		WHERE r.rn = 1
		ORDER BY r.instant DESC, r.conversation_id DESC`
}

// conversationDigestsLegacy executes the parity oracle and scans it exactly
// as the old production read did.
func conversationDigestsLegacy(t *testing.T, s *Store) ConversationDigestSet {
	t.Helper()
	ctx := t.Context()
	vis, err := s.visible(ctx)
	if err != nil {
		t.Fatal(err)
	}
	where := ownerFacingWhere(vis)
	rows, err := s.db.QueryContext(ctx, conversationDigestLegacyQuery(where))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := ConversationDigestSet{Conversations: []ConversationDigest{}}
	for rows.Next() {
		var d ConversationDigest
		var firstAt sql.NullString
		var role string
		var isRead, hasAttachments int
		if err := rows.Scan(&d.ConversationID, &d.Subject, &d.SenderName, &d.SenderAddress,
			&d.LastMessageID, &d.LastRevision, &d.LastAt, &firstAt, &d.Messages, &d.Sent, &d.Unread,
			&role, &isRead, &hasAttachments, &d.DueDate, &d.CounterpartyAddress); err != nil {
			t.Fatal(err)
		}
		d.FirstAt = firstAt.String
		d.Received = d.Messages - d.Sent
		d.LastSent = mail.FolderRole(role) == mail.RoleSent
		d.LastIsRead = isRead != 0
		d.LastHasAttachments = hasAttachments != 0
		d.SearchText = conversationSearchText(d.Subject, d.SenderName, d.SenderAddress)
		out.Conversations = append(out.Conversations, d)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	coverage := ConversationCoverage{Conversations: len(out.Conversations)}
	for _, d := range out.Conversations {
		coverage.Messages += d.Messages
	}
	unthreaded, err := s.unthreadedMessages(ctx, where)
	if err != nil {
		t.Fatal(err)
	}
	coverage.Unthreaded = unthreaded
	coverage.Messages += unthreaded
	out.Coverage = coverage
	return out
}

// TestConversationDigestsMatchesTheLegacyAggregate pins the index-ordered
// stream to the old SQL aggregate on a fixture that exercises every digest
// branch: dated and undated mail, sent and received, attachments, unread,
// equal instants, a completed due date, hidden folders and unthreaded mail.
func TestConversationDigestsMatchesTheLegacyAggregate(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	if err := s.SaveReported(ctx, StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	owner := mail.Recipient{Kind: mail.To, Name: "Owner", Address: mailtest.Owner}
	seed := func(conversationID, id string, mutate func(*mail.Message)) int64 {
		t.Helper()
		return conversationTestMember(t, s, id, conversationID, scope.Owner, func(m *mail.Message) {
			m.SenderName, m.SenderAddress = "Sender "+id, "sender-"+id+"@example.test"
			m.Recipients = []mail.Recipient{owner}
			m.TaskDueDate, m.FlagStatus = "", mail.OptInt{}
			if mutate != nil {
				mutate(m)
			}
		})
	}
	// C-alpha: a dated span, equal instants with a later received row, an
	// attachment, unread received mail and one undated member.
	seed("C-alpha", "alpha-old", func(m *mail.Message) {
		m.Subject = "Alpha old"
		m.SenderName, m.SenderAddress = "Alice", "alice@alpha.test"
		m.ReceivedAt, m.SentOn = "2026-11-03T08:00:00Z", "2026-11-03T08:00:00Z"
		m.IsRead, m.HasAttachments = false, false
		m.TaskDueDate, m.FlagStatus = "2026-11-10T00:00:00Z", mail.Int(2)
	})
	seed("C-alpha", "alpha-sent", func(m *mail.Message) {
		m.Subject = "Alpha owner reply"
		m.FolderRole, m.FolderPath = mail.RoleSent, "Sent"
		m.SenderName, m.SenderAddress = "Owner", mailtest.Owner
		m.ReceivedAt, m.SentOn = "2026-11-03T07:00:00Z", "2026-11-03T10:00:00Z"
		m.IsRead, m.HasAttachments = true, true
	})
	seed("C-alpha", "alpha-latest-received", func(m *mail.Message) {
		m.Subject = "Alpha latest received"
		m.SenderName, m.SenderAddress = "Bob", "bob@alpha.test"
		m.ReceivedAt, m.SentOn = "2026-11-03T10:00:00Z", "2026-11-03T10:00:00Z"
		m.IsRead, m.HasAttachments = false, false
		m.TaskDueDate, m.FlagStatus = "2026-11-12T00:00:00Z", mail.Int(2)
	})
	seed("C-alpha", "alpha-undated", func(m *mail.Message) {
		m.Subject = "Alpha undated"
		m.SenderName, m.SenderAddress = "Carol", "carol@alpha.test"
		m.ReceivedAt, m.SentOn = "", ""
		m.IsRead = true
	})
	// A hidden draft is newer than every member and must not enter either
	// read.
	seed("C-alpha", "alpha-draft", func(m *mail.Message) {
		m.Subject = "Alpha draft"
		m.FolderRole, m.FolderPath = mail.RoleDrafts, "Drafts"
		m.ReceivedAt, m.SentOn = "2026-11-03T23:00:00Z", "2026-11-03T23:00:00Z"
	})

	// C-beta and C-zeta share C-alpha's latest instant: the page order is
	// conversation id descending among equal instants. C-beta's completed
	// due date is empty.
	seed("C-beta", "beta", func(m *mail.Message) {
		m.Subject = "Beta"
		m.SenderName, m.SenderAddress = "Dana", "dana@beta.test"
		m.ReceivedAt, m.SentOn = "2026-11-03T10:00:00Z", "2026-11-03T10:00:00Z"
		m.TaskDueDate, m.FlagStatus = "2026-11-14T00:00:00Z", mail.Int(1)
	})
	seed("C-zeta", "zeta", func(m *mail.Message) {
		m.Subject = "Zeta"
		m.SenderName, m.SenderAddress = "Eve", "eve@zeta.test"
		m.ReceivedAt, m.SentOn = "2026-11-03T10:00:00Z", "2026-11-03T10:00:00Z"
		m.IsRead = true
	})

	// C-undated has no dated member: FirstAt and LastAt stay empty and it
	// sorts after every dated conversation.
	seed("C-undated", "undated-old", func(m *mail.Message) {
		m.Subject = "Undated old"
		m.SenderName, m.SenderAddress = "Frank", "frank@undated.test"
		m.ReceivedAt, m.SentOn = "", ""
		m.IsRead = true
	})
	seed("C-undated", "undated-latest", func(m *mail.Message) {
		m.Subject = "Undated latest"
		m.SenderName, m.SenderAddress = "Grace", "grace@undated.test"
		m.ReceivedAt, m.SentOn = "", ""
		m.IsRead, m.HasAttachments = false, true
	})
	seed("", "unthreaded", func(m *mail.Message) {
		m.Subject = "Unthreaded"
		m.SenderName, m.SenderAddress = "Heidi", "heidi@example.test"
		m.ReceivedAt, m.SentOn = "2026-11-03T12:00:00Z", "2026-11-03T12:00:00Z"
	})

	got, err := s.ConversationDigests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := conversationDigestsLegacy(t, s)
	seen := make(map[string]bool, len(got.Conversations))
	messages := 0
	for _, d := range got.Conversations {
		if seen[d.ConversationID] {
			t.Fatalf("conversation %q appears twice", d.ConversationID)
		}
		seen[d.ConversationID] = true
		messages += d.Messages
	}
	if len(seen) != got.Coverage.Conversations || messages != got.Coverage.Messages-got.Coverage.Unthreaded {
		t.Fatalf("row-count spy: %d conversations, %d messages, coverage %+v", len(seen), messages, got.Coverage)
	}
	gotJSON, err := json.Marshal(got.Conversations)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want.Conversations)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("digest wire shape differs from the legacy aggregate:\n got %s\nwant %s", gotJSON, wantJSON)
	}
	if !reflect.DeepEqual(got.Coverage, want.Coverage) {
		t.Fatalf("coverage %+v, want %+v", got.Coverage, want.Coverage)
	}
}

// TestConversationDigestStreamPlan pins the digest to one index-ordered
// stream: the conversation index carries the grouping and the latest-first
// member order, so the statement needs no temporary sort or materialization.
func TestConversationDigestStreamPlan(t *testing.T) {
	s, _ := open(t, nil)
	conversationTestMember(t, s, "plan-1", "C-plan", scope.Owner, func(m *mail.Message) {
		m.ReceivedAt, m.SentOn = "2026-11-03T08:00:00Z", "2026-11-03T08:00:00Z"
	})
	conversationTestMember(t, s, "plan-2", "C-plan", scope.Owner, func(m *mail.Message) {
		m.ReceivedAt, m.SentOn = "2026-11-03T09:00:00Z", "2026-11-03T09:00:00Z"
	})
	conversationTestMember(t, s, "plan-3", "C-other", scope.Owner, nil)
	vis, err := s.visible(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	plan := priorityQueryPlan(t, s, conversationDigestQuery(ownerFacingWhere(vis)))
	assertIndexOrderedPlan(t, "conversation digest", plan, "messages_conversation_instant")
}
