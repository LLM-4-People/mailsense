package store

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// threadTestMember seeds one conversation member and returns its row id.
func threadTestMember(t *testing.T, s *Store, id string, verdict scope.Verdict, mutate func(*mail.Message)) int64 {
	t.Helper()
	m := mailtest.Message("src", id)
	m.ConversationID = "C-thread"
	m.Subject = "Subject " + id
	m.ReceivedAt, m.SentOn = "2026-11-03T08:00:00Z", "2026-11-03T08:00:00Z"
	if mutate != nil {
		mutate(&m)
	}
	rowID := answerMessage(t, s, m)
	answerVerdict(t, s, rowID, verdict)
	return rowID
}

func TestThreadMessagesOwnerVisibleConversation(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	if err := s.SaveReported(ctx, StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	const client, colleague = "client@example.test", "colleague@example.test"
	owner := mail.Recipient{Kind: mail.To, Name: "Owner", Address: mailtest.Owner}

	firstID := threadTestMember(t, s, "thread-1", scope.Owner, func(m *mail.Message) {
		m.Subject = "First subject"
		m.Body = "First body"
		m.SenderName, m.SenderAddress = "Client", client
		m.SentRepresentingAddress = client
		m.Recipients = []mail.Recipient{owner}
		m.IsRead, m.HasAttachments = true, false
	})
	// A sent item's instant is sent_on, not the delivery-time received_at.
	sentID := threadTestMember(t, s, "thread-2", scope.Owner, func(m *mail.Message) {
		m.Subject = "Owner reply"
		m.Body = "Owner body"
		m.FolderRole, m.FolderPath = mail.RoleSent, "Sent"
		m.SenderName, m.SenderAddress, m.SentRepresentingAddress = "Amélie Durand", mailtest.Owner, mailtest.Owner
		m.AddressedToMe = mail.AddressedNo
		m.Recipients = []mail.Recipient{
			{Kind: mail.To, Name: "Client", Address: client},
			{Kind: mail.Cc, Name: "Colleague", Address: colleague},
			owner,
		}
		m.SentOn, m.ReceivedAt = "2026-11-03T10:00:00Z", "2026-11-03T07:00:00Z"
		m.IsRead, m.HasAttachments = true, false
	})
	latestID := threadTestMember(t, s, "thread-3", scope.Owner, func(m *mail.Message) {
		m.Subject = "Latest subject"
		m.Body = strings.Repeat("alpha\tbeta\n", 200)
		m.SenderName, m.SenderAddress = "Client", client
		m.SentRepresentingAddress = client
		m.Recipients = []mail.Recipient{owner}
		m.ReceivedAt, m.SentOn = "2026-11-03T13:00:00Z", "2026-11-03T13:00:00Z"
		m.IsRead, m.HasAttachments = false, true
	})

	// Every one of these is newer than the visible page; none may leak
	// into its subject, count, span, messages or participants.
	threadTestMember(t, s, "thread-draft", scope.Owner, func(m *mail.Message) {
		m.Subject = "Draft must not win"
		m.FolderRole, m.FolderPath = mail.RoleDrafts, "Drafts"
		m.SenderName, m.SenderAddress = "Amélie Durand", mailtest.Owner
		m.ReceivedAt, m.SentOn = "2026-11-03T15:00:00Z", "2026-11-03T15:00:00Z"
	})
	threadTestMember(t, s, "thread-deleted", scope.Owner, func(m *mail.Message) {
		m.Subject = "Deleted subject"
		m.FolderRole, m.FolderPath = mail.RoleDeleted, "Deleted Items"
		m.SenderName, m.SenderAddress = "Client", client
		m.ReceivedAt, m.SentOn = "2026-11-03T16:00:00Z", "2026-11-03T16:00:00Z"
	})
	threadTestMember(t, s, "thread-shared", scope.Shared, func(m *mail.Message) {
		m.StoreID = "store-2"
		m.Subject = "Shared subject"
		m.SenderName, m.SenderAddress = "Colleague", colleague
		m.ReceivedAt, m.SentOn = "2026-11-03T17:00:00Z", "2026-11-03T17:00:00Z"
	})
	threadTestMember(t, s, "thread-junk", scope.Owner, func(m *mail.Message) {
		m.Subject = "Junk subject"
		m.FolderRole, m.FolderPath = mail.RoleJunk, "Junk Email"
		m.SenderName, m.SenderAddress = "Client", client
		m.ReceivedAt, m.SentOn = "2026-11-03T18:00:00Z", "2026-11-03T18:00:00Z"
	})

	res, err := s.ThreadMessages(ctx, "C-thread", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.ConversationID != "C-thread" || res.Subject != "Latest subject" ||
		res.FirstAt != "2026-11-03T08:00:00Z" || res.LastAt != "2026-11-03T13:00:00Z" ||
		res.Count != 3 || res.Truncated {
		t.Fatalf("header %+v", res)
	}
	wantIDs := []int64{firstID, sentID, latestID}
	if len(res.Messages) != len(wantIDs) {
		t.Fatalf("messages %+v", res.Messages)
	}
	for i, id := range wantIDs {
		if res.Messages[i].ID != id {
			t.Fatalf("message %d id %d, want %d: %+v", i, res.Messages[i].ID, id, res.Messages)
		}
	}
	if got := res.Messages[0]; got.ReceivedAt != "2026-11-03T08:00:00Z" || got.Sent || !got.IsRead || got.HasAttachments {
		t.Fatalf("first message %+v", got)
	}
	sent := res.Messages[1]
	if sent.ReceivedAt != "2026-11-03T10:00:00Z" || !sent.Sent {
		t.Fatalf("sent message must use sent_on: %+v", sent)
	}
	latest := res.Messages[2]
	if latest.Subject != "Latest subject" || latest.ReceivedAt != "2026-11-03T13:00:00Z" ||
		latest.Sent || latest.IsRead || !latest.HasAttachments || latest.ConversationID != "C-thread" {
		t.Fatalf("latest message %+v", latest)
	}
	if utf8.RuneCountInString(latest.Snippet) > threadSnippetRunes {
		t.Fatalf("snippet has %d runes: %q", utf8.RuneCountInString(latest.Snippet), latest.Snippet)
	}
	if strings.ContainsAny(latest.Snippet, "\n\t") || strings.Contains(latest.Snippet, "  ") {
		t.Fatalf("snippet not collapsed: %q", latest.Snippet)
	}

	if len(res.Participants) != 2 {
		t.Fatalf("participants %+v", res.Participants)
	}
	if p := res.Participants[0]; p.Address != client || p.Name != "Client" || p.Messages != 3 ||
		p.LastAt != "2026-11-03T13:00:00Z" {
		t.Fatalf("client participant %+v", p)
	}
	if p := res.Participants[1]; p.Address != colleague || p.Name != "Colleague" || p.Messages != 1 ||
		p.LastAt != "2026-11-03T10:00:00Z" {
		t.Fatalf("colleague participant %+v", p)
	}
	for _, p := range res.Participants {
		if p.Address == mailtest.Owner {
			t.Fatalf("owner leaked into participants: %+v", res.Participants)
		}
	}

	// Page the latest two: they stay chronological, and older mail is
	// disclosed as truncated.
	page, err := s.ThreadMessages(ctx, "C-thread", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 2 || page.Messages[0].ID != sentID || page.Messages[1].ID != latestID || !page.Truncated {
		t.Fatalf("latest page %+v", page)
	}
	if page.Count != 3 || page.Subject != "Latest subject" || len(page.Participants) != 2 {
		t.Fatalf("page conversation facts %+v", page)
	}

	// before is exclusive and pages older, still chronological.
	older, err := s.ThreadMessages(ctx, "C-thread", 1, latestID)
	if err != nil {
		t.Fatal(err)
	}
	if len(older.Messages) != 1 || older.Messages[0].ID != sentID || !older.Truncated {
		t.Fatalf("older page %+v", older)
	}
	older, err = s.ThreadMessages(ctx, "C-thread", 2, latestID)
	if err != nil {
		t.Fatal(err)
	}
	if len(older.Messages) != 2 || older.Messages[0].ID != firstID || older.Messages[1].ID != sentID || older.Truncated {
		t.Fatalf("older page %+v", older)
	}
	empty, err := s.ThreadMessages(ctx, "C-thread", 2, firstID)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Messages) != 0 || empty.Truncated || empty.Count != 3 || empty.Subject != "Latest subject" {
		t.Fatalf("page before the oldest %+v", empty)
	}
	all, err := s.ThreadMessages(ctx, "C-thread", 3, 0)
	if err != nil || len(all.Messages) != 3 || all.Truncated {
		t.Fatalf("full page %+v %v", all, err)
	}
}

// TestThreadMessagesPagesByInstantNotRowID reproduces an initial sync that
// streams newest first: the newest message receives the smallest row id.
// Paging must follow the mail instant, or "load older" reads ids that are
// already displayed and loses the older history.
func TestThreadMessagesPagesByInstantNotRowID(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	if err := s.SaveReported(ctx, StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	owner := mail.Recipient{Kind: mail.To, Name: "Owner", Address: mailtest.Owner}
	seed := func(id, instant, subject string) int64 {
		return threadTestMember(t, s, id, scope.Owner, func(m *mail.Message) {
			m.Subject = subject
			m.SenderName, m.SenderAddress = "Client", "client@example.test"
			m.SentRepresentingAddress = "client@example.test"
			m.Recipients = []mail.Recipient{owner}
			m.ReceivedAt, m.SentOn = instant, instant
		})
	}
	// Newest is seeded first, exactly as the Outlook initial sync does.
	newestID := seed("sync-1", "2026-11-04T09:00:00Z", "Newest")
	secondID := seed("sync-2", "2026-11-03T09:00:00Z", "Second")
	thirdID := seed("sync-3", "2026-11-02T09:00:00Z", "Third")
	oldestID := seed("sync-4", "2026-11-01T09:00:00Z", "Oldest")
	if !(newestID < secondID && secondID < thirdID && thirdID < oldestID) {
		t.Fatalf("seed ids are not reverse chronological: %d %d %d %d", newestID, secondID, thirdID, oldestID)
	}

	page, err := s.ThreadMessages(ctx, "C-thread", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 2 || page.Messages[0].ID != secondID || page.Messages[1].ID != newestID || !page.Truncated {
		t.Fatalf("latest page %+v", page)
	}

	older, err := s.ThreadMessages(ctx, "C-thread", 2, secondID)
	if err != nil {
		t.Fatal(err)
	}
	if len(older.Messages) != 2 || older.Messages[0].ID != oldestID || older.Messages[1].ID != thirdID || older.Truncated {
		t.Fatalf("older page %+v", older)
	}

	if _, err := s.ThreadMessages(ctx, "C-thread", 2, 1<<40); !errors.Is(err, ErrInvalidThreadCursor) {
		t.Fatalf("unknown cursor error = %v, want ErrInvalidThreadCursor", err)
	}

	// Opening an old message anchors the page at that message, inclusively,
	// so its own earlier context is visible instead of newer mail.
	anchored, err := s.ThreadMessagesAt(ctx, "C-thread", 2, thirdID)
	if err != nil {
		t.Fatal(err)
	}
	if len(anchored.Messages) != 2 || anchored.Messages[0].ID != oldestID || anchored.Messages[1].ID != thirdID ||
		anchored.Truncated || anchored.NewestMessageID != newestID || anchored.Newer != 2 {
		t.Fatalf("anchored page %+v", anchored)
	}
	onlyOldest, err := s.ThreadMessagesAt(ctx, "C-thread", 2, oldestID)
	if err != nil {
		t.Fatal(err)
	}
	if len(onlyOldest.Messages) != 1 || onlyOldest.Messages[0].ID != oldestID || onlyOldest.Truncated ||
		onlyOldest.NewestMessageID != newestID || onlyOldest.Newer != 3 {
		t.Fatalf("oldest anchor %+v", onlyOldest)
	}
	latest, err := s.ThreadMessagesAt(ctx, "C-thread", 2, newestID)
	if err != nil {
		t.Fatal(err)
	}
	if len(latest.Messages) != 2 || latest.Messages[0].ID != secondID || latest.Messages[1].ID != newestID ||
		!latest.Truncated || latest.NewestMessageID != newestID || latest.Newer != 0 {
		t.Fatalf("latest anchor %+v", latest)
	}
	if _, err := s.ThreadMessagesAt(ctx, "C-thread", 2, 1<<40); !errors.Is(err, ErrInvalidThreadCursor) {
		t.Fatalf("unknown anchor error = %v, want ErrInvalidThreadCursor", err)
	}
}

// TestThreadMessagesExcludesSharedStoreOwnerContent guards the content
// boundary: a picked shared store can carry its own mailbox owner's mail
// with verdict=owner, but a conversation read may expose only primary
// content. The shared reply still reaches the thread state through
// ThreadStates, never through this read.
func TestThreadMessagesExcludesSharedStoreOwnerContent(t *testing.T) {
	s, _ := open(t, map[string]any{
		source.SettingSelected: []any{source.StoreKey("src", "store-1"), source.StoreKey("src", "store-2")},
	})
	ctx := t.Context()
	if err := s.SaveReported(ctx, StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertFolder(ctx, mail.Folder{SourceID: "src", StoreID: "store-2", StoreName: "Shared",
		Path: "Inbox", Role: mail.RoleInbox, StoreKind: mail.StoreShared}); err != nil {
		t.Fatal(err)
	}
	primary := threadTestMember(t, s, "primary", scope.Owner, func(m *mail.Message) {
		m.Subject = "Owner-visible subject"
		m.SenderName, m.SenderAddress = "Client", "client@example.test"
		m.SentRepresentingAddress = "client@example.test"
	})
	shared := mailtest.Message("src", "shared-owner")
	shared.StoreID, shared.FolderPath = "store-2", "Inbox"
	shared.ConversationID = "C-thread"
	shared.Subject = "Shared subject must not leak"
	shared.SenderName, shared.SenderAddress = "Colleague", "colleague@shared.test"
	shared.SentRepresentingAddress = "colleague@shared.test"
	shared.ReceivedAt, shared.SentOn = "2026-11-03T12:00:00Z", "2026-11-03T12:00:00Z"
	sharedID := answerMessage(t, s, shared)
	answerVerdict(t, s, sharedID, scope.Owner)

	res, err := s.ThreadMessages(ctx, "C-thread", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Count != 1 || len(res.Messages) != 1 || res.Messages[0].ID != primary {
		t.Fatalf("shared content entered the thread read: %+v", res)
	}
	if res.Subject != "Owner-visible subject" || res.NewestMessageID != primary {
		t.Fatalf("shared subject won: %+v", res)
	}
	for _, p := range res.Participants {
		if p.Address == "colleague@shared.test" {
			t.Fatalf("shared participant leaked: %+v", res.Participants)
		}
	}
}

func TestThreadMessagesNotFound(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	if err := s.SaveReported(ctx, StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	// A conversation with only a draft and a shared reply is not owner
	// mail, so it does not exist for this read.
	threadTestMember(t, s, "hidden-draft", scope.Owner, func(m *mail.Message) {
		m.ConversationID = "C-hidden"
		m.FolderRole, m.FolderPath = mail.RoleDrafts, "Drafts"
	})
	threadTestMember(t, s, "hidden-shared", scope.Shared, func(m *mail.Message) {
		m.StoreID = "store-2"
		m.ConversationID = "C-hidden"
	})
	// An owner message of a store that is not picked is outside visibility.
	threadTestMember(t, s, "hidden-store", scope.Owner, func(m *mail.Message) {
		m.StoreID = "store-3"
		m.ConversationID = "C-hidden"
	})

	if _, err := s.ThreadMessages(ctx, "C-unknown", 50, 0); !errors.Is(err, ErrThreadNotFound) {
		t.Fatalf("unknown thread: %v", err)
	}
	if _, err := s.ThreadMessages(ctx, "C-hidden", 50, 0); !errors.Is(err, ErrThreadNotFound) {
		t.Fatalf("draft/shared/unpicked thread: %v", err)
	}
}

func TestThreadMessagesValidation(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	for _, id := range []string{"", strings.Repeat("x", MaxConversationIDChars+1), "bad\x00id", "bad\nid", "bad\x7fid"} {
		if _, err := s.ThreadMessages(ctx, id, 50, 0); !errors.Is(err, ErrInvalidConversationID) {
			t.Errorf("conversation %q: %v", id, err)
		}
	}
	valid := "C1"
	if err := ValidateConversationID(strings.Repeat("é", MaxConversationIDChars)); err != nil {
		t.Errorf("a 512-character id must be valid: %v", err)
	}
	if err := ValidateConversationID(strings.Repeat("x", MaxConversationIDChars)); err != nil {
		t.Errorf("a 512-byte id must be valid: %v", err)
	}
	if _, err := s.ThreadMessages(ctx, valid, 0, 0); err == nil {
		t.Error("a zero limit must fail")
	}
	if _, err := s.ThreadMessages(ctx, valid, MaxThreadMessages+1, 0); err == nil {
		t.Error("an over-max limit must fail")
	}
	if _, err := s.ThreadMessages(ctx, valid, 50, -1); err == nil {
		t.Error("a negative before id must fail")
	}
}
