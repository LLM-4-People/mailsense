package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// messageListSeed writes one owner-visible message and returns its row id.
func messageListSeed(t *testing.T, s *Store, id string, mutate func(*mail.Message)) int64 {
	t.Helper()
	m := mailtest.Message("src", id)
	m.ReceivedAt, m.SentOn = "2026-03-01T09:00:00Z", "2026-03-01T09:00:00Z"
	if mutate != nil {
		mutate(&m)
	}
	rowID := answerMessage(t, s, m)
	answerVerdict(t, s, rowID, scope.Owner)
	return rowID
}

func messageListIDs(page MessageListPage) []int64 {
	out := make([]int64, len(page.Messages))
	for i, row := range page.Messages {
		out[i] = row.RowID
	}
	return out
}

func TestMessageListRolesAllMailAndOrder(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	if err := s.SaveReported(ctx, StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	want := map[mail.FolderRole]int64{}
	seed := func(id string, role mail.FolderRole, received, sent string) {
		t.Helper()
		want[role] = messageListSeed(t, s, id, func(m *mail.Message) {
			m.FolderRole, m.FolderPath = role, string(role)
			m.ReceivedAt, m.SentOn = received, sent
			m.Body = "line one\n\nline two\t" + strings.Repeat("word ", 100)
		})
	}
	seed("inbox", mail.RoleInbox, "2026-03-01T09:00:00Z", "")
	seed("sent", mail.RoleSent, "2026-03-03T09:00:00Z", "2026-03-02T09:00:00Z")
	seed("draft", mail.RoleDrafts, "2026-03-04T09:00:00Z", "")
	seed("deleted", mail.RoleDeleted, "2026-03-05T09:00:00Z", "")
	seed("junk", mail.RoleJunk, "2026-03-06T09:00:00Z", "")
	seed("archive", mail.RoleArchive, "2026-03-07T09:00:00Z", "")
	seed("other", mail.RoleOther, "2026-03-08T09:00:00Z", "")

	all, err := s.MessageList(ctx, MessageListQuery{Role: MessageListAll, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != len(want) || len(all.Messages) != len(want) || all.Counts != nil {
		t.Fatalf("all mail page: total %d rows %d counts %v", all.Total, len(all.Messages), all.Counts)
	}
	// Newest instant first; the sent item sorts by sent_on, not received_at.
	wantOrder := []string{"other", "archive", "junk", "deleted", "draft", "sent", "inbox"}
	for i, row := range all.Messages {
		if row.Subject != "Réunion budget "+wantOrder[i] {
			t.Fatalf("row %d subject %q, want %q", i, row.Subject, wantOrder[i])
		}
	}
	if got := all.Messages[5].Instant; got != "2026-03-02T09:00:00Z" {
		t.Fatalf("sent instant %q, want sent_on", got)
	}
	if got := all.Messages[5].ReceivedAt; got != "2026-03-03T09:00:00Z" {
		t.Fatalf("received_at changed: %q", got)
	}
	if all.Messages[0].StoreKey != source.StoreKey("src", "store-1") || all.Messages[0].StoreName != "Amélie" {
		t.Fatalf("store identity: %+v", all.Messages[0])
	}

	for role, rowID := range want {
		page, err := s.MessageList(ctx, MessageListQuery{Role: role, Limit: 10})
		if err != nil {
			t.Fatalf("%s: %v", role, err)
		}
		if page.Total != 1 || len(page.Messages) != 1 || page.Messages[0].RowID != rowID || page.Messages[0].Role != role {
			t.Fatalf("%s page: %+v", role, page)
		}
	}

	// The list row carries a bounded, collapsed snippet, never recipients,
	// the body, raw HTML or transport headers.
	row := all.Messages[0]
	if utf8.RuneCountInString(row.Snippet) > threadSnippetRunes || strings.ContainsAny(row.Snippet, "\n\t") || strings.Contains(row.Snippet, "  ") {
		t.Fatalf("snippet not bounded/collapsed: %q", row.Snippet)
	}
	raw, err := json.Marshal(all.Messages)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"body", "body_html", "headers", "attachments", "recipients"} {
		if _, ok := rows[0][key]; ok {
			t.Fatalf("list row exposes %q: %s", key, raw)
		}
	}
}

func TestMessageListStoreFilterAndOwnerContentBoundary(t *testing.T) {
	s, _ := open(t, map[string]any{
		source.SettingSelected: mailtest.Picked([]string{"src"}, [2]string{"src", "store-2"}, [2]string{"src", "store-3"}),
	})
	ctx := t.Context()
	for _, ref := range []StoreRef{{SourceID: "src", StoreID: "store-1"}, {SourceID: "src", StoreID: "store-2"}, {SourceID: "src", StoreID: "store-3"}} {
		if err := s.SaveReported(ctx, ref, []string{mailtest.Owner}); err != nil {
			t.Fatal(err)
		}
	}
	for _, store := range []struct {
		id   string
		kind mail.StoreKind
	}{
		{"store-2", mail.StoreShared},
		{"store-3", mail.StoreDelegate},
	} {
		if _, err := s.UpsertFolder(ctx, mail.Folder{SourceID: "src", StoreID: store.id, StoreName: store.id,
			Path: "Inbox", Role: mail.RoleInbox, StoreKind: store.kind}); err != nil {
			t.Fatal(err)
		}
	}
	primary := messageListSeed(t, s, "primary", nil)
	messageListSeed(t, s, "shared", func(m *mail.Message) {
		m.StoreID, m.StoreName = "store-2", "store-2"
	})
	messageListSeed(t, s, "delegate", func(m *mail.Message) {
		m.StoreID, m.StoreName = "store-3", "store-3"
	})

	all, err := s.MessageList(ctx, MessageListQuery{Limit: 20, Counts: true})
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != 1 || len(all.Messages) != 1 || all.Messages[0].RowID != primary {
		t.Fatalf("shared/delegate content leaked: %+v", all.Messages)
	}
	if sum := all.Counts[mail.RoleInbox]; sum != 1 {
		t.Fatalf("counts include shared/delegate: %+v", all.Counts)
	}

	only, err := s.MessageList(ctx, MessageListQuery{Store: source.StoreKey("src", "store-1"), Limit: 20, Counts: true})
	if err != nil {
		t.Fatal(err)
	}
	if only.Total != 1 || len(only.Messages) != 1 || only.Messages[0].RowID != primary ||
		only.Counts[mail.RoleInbox] != 1 {
		t.Fatalf("store filter: %+v", only)
	}
	hidden, err := s.MessageList(ctx, MessageListQuery{Store: source.StoreKey("src", "store-2"), Limit: 20, Counts: true})
	if err != nil {
		t.Fatal(err)
	}
	if hidden.Total != 0 || len(hidden.Messages) != 0 || hidden.Messages == nil || hidden.Counts[mail.RoleInbox] != 0 {
		t.Fatalf("shared store read is not empty: %+v", hidden)
	}

	// The sender filter composes with the same boundary: every seeded row
	// carries the same address, so a matching sender must still not pull in
	// a shared, delegate or unpicked store's mail, and the counts describe
	// the owner rows only.
	messageListSeed(t, s, "unpicked", func(m *mail.Message) {
		m.StoreID, m.StoreName = "store-4", "store-4"
	})
	filtered, err := s.MessageList(ctx, MessageListQuery{Sender: "elodie@example.org", Limit: 20, Counts: true})
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Total != 1 || len(filtered.Messages) != 1 || filtered.Messages[0].RowID != primary ||
		filtered.Counts[mail.RoleInbox] != 1 {
		t.Fatalf("sender filter leaked a shared/delegate/unpicked store: %+v", filtered)
	}
	for _, store := range []string{"store-2", "store-3", "store-4"} {
		hiddenSender, err := s.MessageList(ctx, MessageListQuery{
			Store: source.StoreKey("src", store), Sender: "elodie@example.org", Limit: 20, Counts: true})
		if err != nil {
			t.Fatal(err)
		}
		if hiddenSender.Total != 0 || len(hiddenSender.Messages) != 0 || hiddenSender.Counts[mail.RoleInbox] != 0 {
			t.Fatalf("sender read of %s is not empty: %+v", store, hiddenSender)
		}
	}
}

func TestMessageListSenderFilterAndCounts(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	if err := s.SaveReported(ctx, StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	seed := func(id, address string, role mail.FolderRole, instant string) int64 {
		t.Helper()
		return messageListSeed(t, s, id, func(m *mail.Message) {
			m.SenderAddress, m.SentRepresentingAddress = address, address
			m.FolderRole, m.FolderPath = role, string(role)
			m.ReceivedAt, m.SentOn = instant, instant
		})
	}
	lower := seed("lower", "elodie@example.org", mail.RoleInbox, "2026-03-01T09:00:00Z")
	upper := seed("upper", "Élodie@Example.ORG", mail.RoleSent, "2026-03-02T09:00:00Z")
	tagged := seed("tagged", "elodie+tag@example.org", mail.RoleInbox, "2026-03-03T09:00:00Z")
	other := seed("other", "other@example.org", mail.RoleArchive, "2026-03-04T09:00:00Z")
	sub := seed("sub", "x@sub.example.org", mail.RoleInbox, "2026-03-05T09:00:00Z")
	unicode := seed("unicode", "élodie@exemple.test", mail.RoleInbox, "2026-03-06T09:00:00Z")
	seed("accentless", "elodie@exemple.test", mail.RoleSent, "2026-03-07T09:00:00Z")

	// The exact address folds case; the non-ASCII local part is its own
	// address (case folding is not accent folding).
	exact, err := s.MessageList(ctx, MessageListQuery{Sender: " ELODIE@Example.ORG ", Limit: 10, Counts: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := messageListIDs(exact); len(got) != 1 || got[0] != lower {
		t.Fatalf("exact sender rows %v, want [%d]", got, lower)
	}
	if exact.Total != 1 || exact.Counts[mail.RoleInbox] != 1 || exact.Counts[mail.RoleSent] != 0 {
		t.Fatalf("exact sender total=%d counts=%v", exact.Total, exact.Counts)
	}

	// The non-ASCII exact address: SQLite's ASCII-only lower() would not
	// match it, so the page must use the Go normalization.
	accent, err := s.MessageList(ctx, MessageListQuery{Sender: "ÉLODIE@Example.ORG", Limit: 10, Counts: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := messageListIDs(accent); len(got) != 1 || got[0] != upper {
		t.Fatalf("non-ASCII exact rows %v, want [%d]", got, upper)
	}
	if accent.Total != 1 || accent.Counts[mail.RoleSent] != 1 || accent.Counts[mail.RoleInbox] != 0 {
		t.Fatalf("non-ASCII exact total=%d counts=%v", accent.Total, accent.Counts)
	}

	// A domain is every address under it, never a subdomain.
	domain, err := s.MessageList(ctx, MessageListQuery{Sender: "@Example.Org", Limit: 10, Counts: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := messageListIDs(domain); len(got) != 4 || got[0] != other || got[1] != tagged || got[2] != upper || got[3] != lower {
		t.Fatalf("domain rows %v, want [%d %d %d %d]", got, other, tagged, upper, lower)
	}
	// Counts describe the sender population, not the mailbox.
	if domain.Total != 4 || domain.Counts[mail.RoleInbox] != 2 || domain.Counts[mail.RoleSent] != 1 || domain.Counts[mail.RoleArchive] != 1 {
		t.Fatalf("domain total=%d counts=%v", domain.Total, domain.Counts)
	}
	if domain.Counts[mail.RoleDrafts] != 0 || len(domain.Counts) != len(mail.Roles()) {
		t.Fatalf("domain counts are not the zero-filled sender counts: %v", domain.Counts)
	}

	// Another non-ASCII exact address in a different domain.
	accentExemple, err := s.MessageList(ctx, MessageListQuery{Sender: "ÉLODIE@EXEMPLE.TEST", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := messageListIDs(accentExemple); len(got) != 1 || got[0] != unicode {
		t.Fatalf("non-ASCII exact rows %v, want [%d]", got, unicode)
	}

	// A plus tag is a different exact address, but the same domain.
	tag, err := s.MessageList(ctx, MessageListQuery{Sender: "elodie+tag@example.org", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := messageListIDs(tag); len(got) != 1 || got[0] != tagged {
		t.Fatalf("plus-tag rows %v, want [%d]", got, tagged)
	}
	subdomain, err := s.MessageList(ctx, MessageListQuery{Sender: "@sub.example.org", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := messageListIDs(subdomain); len(got) != 1 || got[0] != sub {
		t.Fatalf("subdomain rows %v, want [%d]", got, sub)
	}

	// A sender that matches nothing is an empty page with zero counts, not
	// an error and not the unfiltered mailbox.
	none, err := s.MessageList(ctx, MessageListQuery{Sender: "nobody@example.net", Limit: 10, Counts: true})
	if err != nil {
		t.Fatal(err)
	}
	if none.Total != 0 || len(none.Messages) != 0 || none.Messages == nil || none.HasMore || none.NextBefore != "" {
		t.Fatalf("empty sender page: %+v", none)
	}
	for role, n := range none.Counts {
		if n != 0 {
			t.Fatalf("empty sender counts %s=%d", role, n)
		}
	}
	if len(none.Counts) != len(mail.Roles()) {
		t.Fatalf("empty sender counts are not zero-filled: %v", none.Counts)
	}

	// No sender means no sender predicate: the default page keeps every row
	// and never invokes the SQL scalar.
	if cond, args := senderWhere(""); cond != "" || len(args) != 0 {
		t.Fatalf("empty sender installed a predicate: %q %v", cond, args)
	}
	all, err := s.MessageList(ctx, MessageListQuery{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != 7 || len(all.Messages) != 7 {
		t.Fatalf("default page total=%d rows=%d", all.Total, len(all.Messages))
	}
}

func TestMessageListUnreadFilterAndCounts(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	if err := s.SaveReported(ctx, StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	seed := func(id string, role mail.FolderRole, read bool, instant string) int64 {
		t.Helper()
		return messageListSeed(t, s, id, func(m *mail.Message) {
			m.FolderRole, m.FolderPath = role, string(role)
			m.ReceivedAt, m.SentOn = instant, instant
			m.IsRead = read
		})
	}
	inboxUnread := seed("inbox-unread", mail.RoleInbox, false, "2026-03-01T09:00:00Z")
	sentUnread := seed("sent-unread", mail.RoleSent, false, "2026-03-02T09:00:00Z")
	draftUnread := seed("draft-unread", mail.RoleDrafts, false, "2026-03-03T09:00:00Z")
	seed("inbox-read", mail.RoleInbox, true, "2026-03-04T09:00:00Z")
	seed("sent-read", mail.RoleSent, true, "2026-03-05T09:00:00Z")

	// An absent Unread keeps every read state and the same whole-mailbox
	// counts: false and absent are one filter-off state.
	anyState, err := s.MessageList(ctx, MessageListQuery{Limit: 10, Counts: true})
	if err != nil {
		t.Fatal(err)
	}
	if anyState.Total != 5 || len(anyState.Messages) != 5 ||
		anyState.Counts[mail.RoleInbox] != 2 || anyState.Counts[mail.RoleSent] != 2 ||
		anyState.Counts[mail.RoleDrafts] != 1 {
		t.Fatalf("any-state page: total=%d rows=%d counts=%v", anyState.Total, len(anyState.Messages), anyState.Counts)
	}

	// Unread=true filters the page and its counts to unread mail. The
	// counts still ignore Role but keep the read state, newest first.
	unread, err := s.MessageList(ctx, MessageListQuery{Unread: true, Limit: 10, Counts: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := messageListIDs(unread), []int64{draftUnread, sentUnread, inboxUnread}; len(got) != len(want) ||
		got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("unread rows %v, want %v", got, want)
	}
	if unread.Total != 3 || unread.Counts[mail.RoleInbox] != 1 || unread.Counts[mail.RoleSent] != 1 ||
		unread.Counts[mail.RoleDrafts] != 1 || unread.Counts[mail.RoleArchive] != 0 || len(unread.Counts) != len(mail.Roles()) {
		t.Fatalf("unread total=%d counts=%v", unread.Total, unread.Counts)
	}
	for _, row := range unread.Messages {
		if row.IsRead {
			t.Fatalf("unread page returned a read row: %+v", row)
		}
	}

	// The read state composes with the role filter: the page keeps both
	// predicates while the counts keep only the read state.
	drafts, err := s.MessageList(ctx, MessageListQuery{Role: mail.RoleDrafts, Unread: true, Limit: 10, Counts: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := messageListIDs(drafts); len(got) != 1 || got[0] != draftUnread {
		t.Fatalf("unread drafts rows %v, want [%d]", got, draftUnread)
	}
	if drafts.Total != 1 || drafts.Counts[mail.RoleDrafts] != 1 || drafts.Counts[mail.RoleInbox] != 1 ||
		drafts.Counts[mail.RoleSent] != 1 {
		t.Fatalf("unread drafts total=%d counts=%v", drafts.Total, drafts.Counts)
	}

	// The role counts ignore Role, not the read state: an unread sent folder
	// still reports the unread inbox and drafts rows behind its counts.
	sent, err := s.MessageList(ctx, MessageListQuery{Role: mail.RoleSent, Unread: true, Limit: 10, Counts: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := messageListIDs(sent); len(got) != 1 || got[0] != sentUnread {
		t.Fatalf("unread sent rows %v, want [%d]", got, sentUnread)
	}
	if sent.Total != 1 || sent.Counts[mail.RoleInbox] != 1 || sent.Counts[mail.RoleDrafts] != 1 {
		t.Fatalf("unread sent total=%d counts=%v", sent.Total, sent.Counts)
	}
}

func TestMessageListCountsCacheSeparatesUnread(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	if err := s.SaveReported(ctx, StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	seed := func(id string, role mail.FolderRole, read bool) {
		t.Helper()
		messageListSeed(t, s, id, func(m *mail.Message) {
			m.FolderRole, m.FolderPath = role, string(role)
			m.IsRead = read
		})
	}
	seed("inbox-read-a", mail.RoleInbox, true)
	seed("inbox-read-b", mail.RoleInbox, true)
	seed("inbox-unread", mail.RoleInbox, false)
	seed("sent-unread", mail.RoleSent, false)

	anyState, err := s.MessageList(ctx, MessageListQuery{Limit: 10, Counts: true})
	if err != nil {
		t.Fatal(err)
	}
	if anyState.Total != 4 || anyState.Counts[mail.RoleInbox] != 3 || anyState.Counts[mail.RoleSent] != 1 {
		t.Fatalf("any counts: total=%d %v", anyState.Total, anyState.Counts)
	}
	// A different read state must not reuse the cached unfiltered counts.
	unread, err := s.MessageList(ctx, MessageListQuery{Unread: true, Limit: 10, Counts: true})
	if err != nil {
		t.Fatal(err)
	}
	if unread.Total != 2 || unread.Counts[mail.RoleInbox] != 1 || unread.Counts[mail.RoleSent] != 1 {
		t.Fatalf("unread reused the any-state counts: total=%d %v", unread.Total, unread.Counts)
	}
	// The unfiltered entry still holds its own numbers.
	anyAgain, err := s.MessageList(ctx, MessageListQuery{Limit: 10, Counts: true})
	if err != nil {
		t.Fatal(err)
	}
	if anyAgain.Total != 4 || anyAgain.Counts[mail.RoleInbox] != 3 || anyAgain.Counts[mail.RoleSent] != 1 {
		t.Fatalf("unread poisoned the any-state counts: total=%d %v", anyAgain.Total, anyAgain.Counts)
	}
}

func TestMessageListCountsCacheSeparatesSenders(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	if err := s.SaveReported(ctx, StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	seed := func(id, address string, role mail.FolderRole) {
		t.Helper()
		messageListSeed(t, s, id, func(m *mail.Message) {
			m.SenderAddress, m.SentRepresentingAddress = address, address
			m.FolderRole, m.FolderPath = role, string(role)
		})
	}
	seed("a-1", "alice@example.org", mail.RoleInbox)
	seed("a-2", "alice@example.org", mail.RoleInbox)
	seed("b-1", "bob@example.org", mail.RoleSent)
	seed("b-2", "bob@example.org", mail.RoleSent)
	seed("b-3", "bob@example.org", mail.RoleSent)

	alice, err := s.MessageList(ctx, MessageListQuery{Sender: "alice@example.org", Limit: 10, Counts: true})
	if err != nil {
		t.Fatal(err)
	}
	if alice.Total != 2 || alice.Counts[mail.RoleInbox] != 2 || alice.Counts[mail.RoleSent] != 0 {
		t.Fatalf("alice counts: total=%d %v", alice.Total, alice.Counts)
	}
	bob, err := s.MessageList(ctx, MessageListQuery{Sender: "bob@example.org", Limit: 10, Counts: true})
	if err != nil {
		t.Fatal(err)
	}
	if bob.Total != 3 || bob.Counts[mail.RoleSent] != 3 || bob.Counts[mail.RoleInbox] != 0 {
		t.Fatalf("bob reused another sender's counts: total=%d %v", bob.Total, bob.Counts)
	}
	// The second alice read must still be alice's, from the cache.
	aliceAgain, err := s.MessageList(ctx, MessageListQuery{Sender: "alice@example.org", Limit: 10, Counts: true})
	if err != nil {
		t.Fatal(err)
	}
	if aliceAgain.Total != 2 || aliceAgain.Counts[mail.RoleInbox] != 2 || aliceAgain.Counts[mail.RoleSent] != 0 {
		t.Fatalf("alice cache entry was poisoned: total=%d %v", aliceAgain.Total, aliceAgain.Counts)
	}
}

func TestMessageListCountsIgnoreRoleButKeepStore(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	if err := s.SaveReported(ctx, StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	messageListSeed(t, s, "a", nil)
	messageListSeed(t, s, "b", func(m *mail.Message) {
		m.FolderRole, m.FolderPath = mail.RoleSent, "Sent"
		m.SentOn = "2026-03-02T09:00:00Z"
	})
	messageListSeed(t, s, "c", func(m *mail.Message) {
		m.FolderRole, m.FolderPath = mail.RoleArchive, "Archive"
	})
	page, err := s.MessageList(ctx, MessageListQuery{Role: mail.RoleInbox, Limit: 10, Counts: true})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Messages) != 1 {
		t.Fatalf("role page: %+v", page)
	}
	if page.Counts[mail.RoleInbox] != 1 || page.Counts[mail.RoleSent] != 1 || page.Counts[mail.RoleArchive] != 1 ||
		page.Counts[mail.RoleDrafts] != 0 || len(page.Counts) != len(mail.Roles()) {
		t.Fatalf("role counts: %+v", page.Counts)
	}
}

func TestMessageListCursorIsStableAndFilterBound(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	if err := s.SaveReported(ctx, StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		ids = append(ids, messageListSeed(t, s, id, func(m *mail.Message) {
			m.ReceivedAt, m.SentOn = "2026-03-01T09:00:00Z", "2026-03-01T09:00:00Z" // one instant: id is the tie-break
		}))
	}
	var seen []int64
	before := Cursor{}
	for page := 0; page < 3; page++ {
		got, err := s.MessageList(ctx, MessageListQuery{Before: before, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, messageListIDs(got)...)
		if page < 2 {
			if !got.HasMore || got.NextBefore == "" || len(got.Messages) != 2 {
				t.Fatalf("page %d: %+v", page, got)
			}
			before, err = ParseCursor(got.NextBefore)
			if err != nil {
				t.Fatal(err)
			}
			if before.Role != "" || before.Store != "" {
				t.Fatalf("cursor filter: %+v", before)
			}
		} else if got.HasMore || got.NextBefore != "" || len(got.Messages) != 1 {
			t.Fatalf("last page: %+v", got)
		}
	}
	want := []int64{ids[4], ids[3], ids[2], ids[1], ids[0]}
	if len(seen) != len(want) {
		t.Fatalf("seen %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("seen %v, want %v", seen, want)
		}
	}

	first, err := s.MessageList(ctx, MessageListQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	cursor, _ := ParseCursor(first.NextBefore)
	if _, err := s.MessageList(ctx, MessageListQuery{Role: mail.RoleInbox, Before: cursor, Limit: 2}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("role-mismatched cursor error %v", err)
	}
	if _, err := s.MessageList(ctx, MessageListQuery{Store: source.StoreKey("src", "store-1"), Before: cursor, Limit: 2}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("store-mismatched cursor error %v", err)
	}

	// A sender page carries its normalized sender, and that cursor pages
	// only the same sender. A cursor issued before a filter can never page
	// a filtered list, and one sender's cursor cannot page another's.
	senderPage, err := s.MessageList(ctx, MessageListQuery{Sender: "ELODIE@Example.ORG", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !senderPage.HasMore || senderPage.NextBefore == "" {
		t.Fatalf("sender first page: %+v", senderPage)
	}
	senderCursor, err := ParseCursor(senderPage.NextBefore)
	if err != nil {
		t.Fatal(err)
	}
	if senderCursor.Sender != "elodie@example.org" {
		t.Fatalf("sender cursor %q, want the normalized sender", senderCursor.Sender)
	}
	next, err := s.MessageList(ctx, MessageListQuery{Sender: "elodie@example.org", Before: senderCursor, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Messages) != 2 || next.Messages[0].RowID != ids[2] || next.Messages[1].RowID != ids[1] {
		t.Fatalf("sender second page ignored its cursor: %+v", next.Messages)
	}
	if _, err := s.MessageList(ctx, MessageListQuery{Sender: "elodie@example.org", Before: cursor, Limit: 2}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("unfiltered cursor paged a sender query: %v", err)
	}
	if _, err := s.MessageList(ctx, MessageListQuery{Before: senderCursor, Limit: 2}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("sender cursor paged the unfiltered list: %v", err)
	}
	if _, err := s.MessageList(ctx, MessageListQuery{Sender: "other@example.org", Before: senderCursor, Limit: 2}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("sender A cursor paged sender B: %v", err)
	}

	// The read state is part of the cursor contract too. Mark the three
	// newest rows unread and page that population; the cursor carries the
	// one state and cannot page the other state.
	for _, id := range []string{"e", "d", "c"} {
		m := mailtest.Message("src", id)
		m.IsRead = false
		m.ReceivedAt, m.SentOn = "2026-03-01T09:00:00Z", "2026-03-01T09:00:00Z"
		if err := s.UpsertMessages(ctx, 0, []mail.Message{m}); err != nil {
			t.Fatal(err)
		}
	}
	unreadPage, err := s.MessageList(ctx, MessageListQuery{Unread: true, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := messageListIDs(unreadPage); len(got) != 2 || got[0] != ids[4] || got[1] != ids[3] {
		t.Fatalf("unread first page %v, want [%d %d]", got, ids[4], ids[3])
	}
	unreadCursor, err := ParseCursor(unreadPage.NextBefore)
	if err != nil {
		t.Fatal(err)
	}
	if !unreadCursor.Unread {
		t.Fatalf("unread cursor lost its state: %+v", unreadCursor)
	}
	unreadNext, err := s.MessageList(ctx, MessageListQuery{Unread: true, Before: unreadCursor, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := messageListIDs(unreadNext); len(got) != 1 || got[0] != ids[2] {
		t.Fatalf("unread second page %v, want [%d]", got, ids[2])
	}
	for _, q := range []MessageListQuery{
		{Before: unreadCursor, Limit: 2},
		{Unread: false, Before: unreadCursor, Limit: 2},
	} {
		if _, err := s.MessageList(ctx, q); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("unread cursor paged %+v: %v", q, err)
		}
	}
	if _, err := s.MessageList(ctx, MessageListQuery{Unread: true, Before: cursor, Limit: 2}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("an unfiltered cursor paged the unread list: %v", err)
	}
	// False and absent are the same filter-off state: the plain cursor still
	// pages the unfiltered list.
	if _, err := s.MessageList(ctx, MessageListQuery{Unread: false, Before: cursor, Limit: 2}); err != nil {
		t.Fatalf("filter-off cursor did not page the unfiltered list: %v", err)
	}
	for _, raw := range []string{"", "!!!", Cursor{ID: 1, Instant: "not-a-time"}.String(), Cursor{Instant: "2026-03-01T09:00:00Z"}.String(),
		Cursor{ID: 1, Role: "nope"}.String(), Cursor{ID: 1, Store: "store-1"}.String(),
		Cursor{ID: 1, Sender: "@"}.String(), Cursor{ID: 1, Sender: "  "}.String(),
		Cursor{ID: 1, Sender: "Not@Normalized"}.String()} {
		if _, err := ParseCursor(raw); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("cursor %q: %v", raw, err)
		}
	}

	// A row inserted after page one never duplicates a row already shown.
	// A newer row is above the cursor and intentionally not in the older
	// pages; an older row still appears once.
	newer := messageListSeed(t, s, "newer", func(m *mail.Message) {
		m.ReceivedAt, m.SentOn = "2026-03-02T09:00:00Z", "2026-03-02T09:00:00Z"
	})
	older := messageListSeed(t, s, "older", func(m *mail.Message) {
		m.ReceivedAt, m.SentOn = "2026-02-01T09:00:00Z", "2026-02-01T09:00:00Z"
	})
	after, err := s.MessageList(ctx, MessageListQuery{Before: cursor, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	got := messageListIDs(after)
	for _, id := range got {
		if id == first.Messages[0].RowID || id == first.Messages[1].RowID || id == newer {
			t.Fatalf("insert produced a duplicate or newer row %d: first %v then %v", id, messageListIDs(first), got)
		}
	}
	if len(got) != 4 || got[3] != older {
		t.Fatalf("insert after first page skipped an older row: %v", got)
	}
}

func TestMessageListValidationBoundsAndEmpty(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	empty, err := s.MessageList(ctx, MessageListQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Total != 0 || empty.Messages == nil || len(empty.Messages) != 0 || empty.HasMore || empty.NextBefore != "" {
		t.Fatalf("empty page: %+v", empty)
	}
	for _, tc := range []struct {
		name string
		q    MessageListQuery
		want error
	}{
		{"zero limit defaults", MessageListQuery{Limit: 0}, nil},
		{"min limit", MessageListQuery{Limit: 1}, nil},
		{"max limit", MessageListQuery{Limit: 200}, nil},
		{"negative limit", MessageListQuery{Limit: -1}, ErrInvalidLimit},
		{"over max limit", MessageListQuery{Limit: 201}, ErrInvalidLimit},
		{"bad role", MessageListQuery{Role: "inboxx"}, ErrInvalidRole},
		{"bad store", MessageListQuery{Store: "store-1"}, ErrInvalidStore},
		{"blank sender", MessageListQuery{Sender: "   "}, ErrInvalidSender},
		{"lone at", MessageListQuery{Sender: "@"}, ErrInvalidSender},
		{"control sender", MessageListQuery{Sender: "a\x00b@example.org"}, ErrInvalidSender},
		{"over max sender", MessageListQuery{Sender: strings.Repeat("a", mail.MaxSenderQueryRunes+1)}, ErrInvalidSender},
		{"mixed-case sender", MessageListQuery{Sender: " Demo@Example.Test "}, nil},
		{"unread cursor on the unfiltered list", MessageListQuery{Before: Cursor{ID: 1, Unread: true}}, ErrInvalidCursor},
		{"filter-off cursor on the unfiltered list", MessageListQuery{Before: Cursor{ID: 1, Unread: false}}, nil},
		{"unread query without a cursor", MessageListQuery{Unread: true}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.MessageList(ctx, tc.q)
			if tc.want == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("error %v, want %v", err, tc.want)
			}
		})
	}
}

func TestInstantColumnMatchesMailInstant(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	messageListSeed(t, s, "inbox", func(m *mail.Message) {
		m.ReceivedAt, m.SentOn = "2026-03-03T09:00:00Z", "2026-03-02T09:00:00Z"
	})
	messageListSeed(t, s, "sent", func(m *mail.Message) {
		m.FolderRole, m.FolderPath = mail.RoleSent, "Sent"
		m.ReceivedAt, m.SentOn = "2026-03-05T09:00:00Z", "2026-03-04T09:00:00Z"
	})
	messageListSeed(t, s, "sent-undated", func(m *mail.Message) {
		m.FolderRole, m.FolderPath = mail.RoleSent, "Sent"
		m.ReceivedAt, m.SentOn = "2026-03-06T09:00:00Z", ""
	})
	rows, err := s.db.QueryContext(ctx, `SELECT m.id, m.instant, m.received_at, m.sent_on, f.role
		FROM messages m JOIN folders f ON f.id = m.folder_id ORDER BY m.id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var instant, received, sent, role string
		if err := rows.Scan(&id, &instant, &received, &sent, &role); err != nil {
			t.Fatal(err)
		}
		at, ok := mail.Instant(mail.FolderRole(role), received, sent)
		want := ""
		if ok {
			want = at.UTC().Format(mail.TimeLayout)
		}
		if instant != want {
			t.Fatalf("row %d instant %q, mail.Instant %q", id, instant, want)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	// A rewrite recomputes the column: once sent it uses sent_on, then an
	// empty sent_on falls back to received_at.
	mutate := mailtest.Message("src", "inbox")
	mutate.FolderRole, mutate.FolderPath = mail.RoleSent, "Sent"
	mutate.ReceivedAt, mutate.SentOn = "2026-03-09T09:00:00Z", "2026-03-08T09:00:00Z"
	if err := s.UpsertMessages(ctx, 0, []mail.Message{mutate}); err != nil {
		t.Fatal(err)
	}
	var instant string
	if err := s.db.QueryRowContext(ctx, "SELECT instant FROM messages WHERE msg_id = 'inbox'").Scan(&instant); err != nil {
		t.Fatal(err)
	}
	if instant != "2026-03-08T09:00:00Z" {
		t.Fatalf("rewritten instant %q", instant)
	}
	mutate.SentOn = ""
	if err := s.UpsertMessages(ctx, 0, []mail.Message{mutate}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT instant FROM messages WHERE msg_id = 'inbox'").Scan(&instant); err != nil {
		t.Fatal(err)
	}
	if instant != "2026-03-09T09:00:00Z" {
		t.Fatalf("fallback instant %q", instant)
	}

	// A partial summary write still computes the one instant from the
	// merged message: a sent copy keeps sent_on even when the delivery
	// stamp moves.
	partial := mail.Message{
		SourceID: "src", StoreID: "store-1", ID: "sent", FolderRole: mail.RoleSent, FolderPath: "Sent",
		ReceivedAt: "2026-04-01T09:00:00Z", SentOn: "2026-03-04T09:00:00Z", Partial: true,
	}
	if err := s.UpsertMessages(ctx, 0, []mail.Message{partial}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT instant FROM messages WHERE msg_id = 'sent'").Scan(&instant); err != nil {
		t.Fatal(err)
	}
	if instant != "2026-03-04T09:00:00Z" {
		t.Fatalf("partial write instant %q", instant)
	}
}

func TestMessageListIndexesExistAndOrderByUsesInstant(t *testing.T) {
	s, _ := open(t, nil)
	var names []string
	rows, err := s.db.Query("SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = 'messages'")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	_ = rows.Close()
	for _, want := range []string{"messages_instant", "messages_folder_instant"} {
		found := false
		for _, name := range names {
			found = found || name == want
		}
		if !found {
			t.Fatalf("index %s missing from %v", want, names)
		}
	}
	planRows, err := s.db.Query("EXPLAIN QUERY PLAN SELECT id FROM messages ORDER BY instant DESC, id DESC LIMIT 5")
	if err != nil {
		t.Fatal(err)
	}
	defer planRows.Close()
	var plan strings.Builder
	for planRows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := planRows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail)
		plan.WriteString("\n")
	}
	if !strings.Contains(plan.String(), "messages_instant") {
		t.Fatalf("order does not use the instant index:\n%s", plan.String())
	}

	// The list's real join must keep the same order without a temporary
	// sort: a keyset page cannot afford to sort the mailbox per request.
	full := `EXPLAIN QUERY PLAN SELECT m.id FROM messages m INDEXED BY messages_instant
		JOIN folders f ON f.id = m.folder_id
		JOIN scope_verdicts v ON v.message_id = m.id
		WHERE f.active = 1 AND f.picked = 1 AND v.verdict = 'owner'
		ORDER BY m.instant DESC, m.id DESC LIMIT 101`
	fullRows, err := s.db.Query(full)
	if err != nil {
		t.Fatal(err)
	}
	defer fullRows.Close()
	var fullPlan strings.Builder
	for fullRows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := fullRows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		fullPlan.WriteString(detail)
		fullPlan.WriteString("\n")
	}
	if !strings.Contains(fullPlan.String(), "messages_instant") || strings.Contains(fullPlan.String(), "TEMP B-TREE") {
		t.Fatalf("the list does not follow the keyset index:\n%s", fullPlan.String())
	}

	// A sender page reads the stored sender key's index, so the predicate
	// and the page order are one range read: no scan of the owner
	// population and no temporary sort.
	if senderFilterOf("").index != messageListOrderIndex ||
		senderFilterOf("demo@example.test").index != messageListSenderNormIndex ||
		senderFilterOf("@example.test").index != messageListSenderDomainIndex ||
		senderFilterOf("example.test").index != messageListSenderDomainIndex {
		t.Fatal("the sender filter does not choose its page index")
	}
	senderPlan := `EXPLAIN QUERY PLAN SELECT m.id` + messageListPageFrom(senderFilterOf("demo@example.test").index) + `
		WHERE f.active = 1 AND f.picked = 1 AND v.verdict = 'owner'
		AND m.sender_norm = 'demo@example.test'
		ORDER BY m.instant DESC, m.id DESC LIMIT 101`
	senderRows, err := s.db.Query(senderPlan)
	if err != nil {
		t.Fatal(err)
	}
	defer senderRows.Close()
	var senderBuilder strings.Builder
	for senderRows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := senderRows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		senderBuilder.WriteString(detail)
		senderBuilder.WriteString("\n")
	}
	if !strings.Contains(senderBuilder.String(), "messages_sender_norm") || strings.Contains(senderBuilder.String(), "TEMP B-TREE") {
		t.Fatalf("the sender page does not read the sender index:\n%s", senderBuilder.String())
	}
	// The domain form is the same range read on its own index.
	domainPlan := `EXPLAIN QUERY PLAN SELECT m.id` + messageListPageFrom(senderFilterOf("@example.test").index) + `
		WHERE f.active = 1 AND f.picked = 1 AND v.verdict = 'owner'
		AND m.sender_domain = 'example.test'
		ORDER BY m.instant DESC, m.id DESC LIMIT 101`
	domainRows, err := s.db.Query(domainPlan)
	if err != nil {
		t.Fatal(err)
	}
	defer domainRows.Close()
	var domainBuilder strings.Builder
	for domainRows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := domainRows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		domainBuilder.WriteString(detail)
		domainBuilder.WriteString("\n")
	}
	if !strings.Contains(domainBuilder.String(), "messages_sender_domain") || strings.Contains(domainBuilder.String(), "TEMP B-TREE") {
		t.Fatalf("the domain page does not read the domain index:\n%s", domainBuilder.String())
	}

	// An unread page keeps the same order: the read-state predicate is
	// applied over the indexed scan. No is_read index is shipped in this
	// round, so the plan must still start from messages_instant.
	unreadPlan := `EXPLAIN QUERY PLAN SELECT m.id FROM messages m INDEXED BY messages_instant
		JOIN folders f ON f.id = m.folder_id
		JOIN scope_verdicts v ON v.message_id = m.id
		WHERE f.active = 1 AND f.picked = 1 AND v.verdict = 'owner'
		AND m.is_read = 0
		ORDER BY m.instant DESC, m.id DESC LIMIT 101`
	unreadRows, err := s.db.Query(unreadPlan)
	if err != nil {
		t.Fatal(err)
	}
	defer unreadRows.Close()
	var unreadBuilder strings.Builder
	for unreadRows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := unreadRows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		unreadBuilder.WriteString(detail)
		unreadBuilder.WriteString("\n")
	}
	if !strings.Contains(unreadBuilder.String(), "messages_instant") || strings.Contains(unreadBuilder.String(), "TEMP B-TREE") {
		t.Fatalf("the unread page does not follow the keyset index:\n%s", unreadBuilder.String())
	}
	for _, name := range names {
		if strings.Contains(name, "is_read") {
			t.Fatalf("an is_read index appeared: %s", name)
		}
	}
}

func TestMessageListCountsCacheFollowsWritesAndDerivesTotal(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	if err := s.SaveReported(ctx, StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	messageListSeed(t, s, "inbox-a", nil)
	messageListSeed(t, s, "inbox-b", nil)

	all, err := s.MessageList(ctx, MessageListQuery{Limit: 10, Counts: true})
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != 2 || all.Counts[mail.RoleInbox] != 2 {
		t.Fatalf("all total=%d counts=%v", all.Total, all.Counts)
	}
	// A cache hit serves the same numbers.
	again, err := s.MessageList(ctx, MessageListQuery{Limit: 10, Counts: true})
	if err != nil {
		t.Fatal(err)
	}
	if again.Total != all.Total || again.Counts[mail.RoleInbox] != all.Counts[mail.RoleInbox] {
		t.Fatalf("cache changed the counts: %+v / %+v", all, again)
	}
	inbox, err := s.MessageList(ctx, MessageListQuery{Role: mail.RoleInbox, Limit: 10, Counts: true})
	if err != nil {
		t.Fatal(err)
	}
	if inbox.Total != inbox.Counts[mail.RoleInbox] || inbox.Total != 2 {
		t.Fatalf("role total=%d counts=%v", inbox.Total, inbox.Counts)
	}

	// A write bumps the generation: the next read must not reuse the old
	// counts, even though the cache key is the same store.
	messageListSeed(t, s, "inbox-c", nil)
	after, err := s.MessageList(ctx, MessageListQuery{Limit: 10, Counts: true})
	if err != nil {
		t.Fatal(err)
	}
	if after.Total != 3 || after.Counts[mail.RoleInbox] != 3 {
		t.Fatalf("a write did not invalidate the counts: total=%d counts=%v", after.Total, after.Counts)
	}
}

// TestMessageListRolePageReadsOneFolderRange pins the role page's access
// path: one pinned folder is a keyset range on messages_folder_instant, so
// the read stops after limit+1 rows instead of walking the whole instant
// index for a role with no owner rows. A temporary sort here would mean the
// page had fallen back to sorting the role population.
func TestMessageListRolePageReadsOneFolderRange(t *testing.T) {
	s, _ := open(t, nil)
	rowID := messageListSeed(t, s, "inbox", func(m *mail.Message) {
		m.FolderRole, m.FolderPath = mail.RoleInbox, "Inbox"
	})
	var folderID int64
	if err := s.db.QueryRow("SELECT folder_id FROM messages WHERE id = ?", rowID).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	plan := priorityQueryPlan(t, s, messageListQuery(messageListFolderIndex,
		visibleSQL+" AND "+ownerContentWhere+" AND m.folder_id = ?"), folderID, MessageListDefaultLimit+1)
	for _, line := range []string{"messages_folder_instant", "SEARCH m USING INDEX messages_folder_instant (folder_id=?)"} {
		if !strings.Contains(plan, line) {
			t.Fatalf("the role page does not read the folder range (%q):\n%s", line, plan)
		}
	}
	for _, banned := range []string{"TEMP B-TREE", "SCAN m"} {
		if strings.Contains(plan, banned) {
			t.Fatalf("the role page plan uses %s:\n%s", banned, plan)
		}
	}
	// The role resolver reads only the folder table; it never touches mail.
	folders := priorityQueryPlan(t, s, `SELECT f.id FROM folders f WHERE `+visibleSQL+` AND `+messageListRoleWhere+` ORDER BY f.id`, string(mail.RoleInbox))
	if strings.Contains(folders, "messages") || strings.Contains(folders, "TEMP B-TREE") {
		t.Fatalf("the role resolver reads mail or sorts:\n%s", folders)
	}
}

// TestMessageListRolePageMergesFoldersInOrder proves the role page keeps one
// global order when the role lives in more than one folder: each folder is a
// range read, and the page is the newest limit+1 of their union.
func TestMessageListRolePageMergesFoldersInOrder(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	if err := s.SaveReported(ctx, StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	seed := func(id, path, instant string, role mail.FolderRole) int64 {
		t.Helper()
		return messageListSeed(t, s, id, func(m *mail.Message) {
			m.FolderRole, m.FolderPath = role, path
			m.ReceivedAt, m.SentOn = instant, instant
		})
	}
	a := seed("a", "Inbox one", "2026-03-01T09:00:00Z", mail.RoleInbox)
	b := seed("b", "Inbox two", "2026-03-02T09:00:00Z", mail.RoleInbox)
	c := seed("c", "Inbox one", "2026-03-03T09:00:00Z", mail.RoleInbox)
	d := seed("d", "Inbox two", "2026-03-04T09:00:00Z", mail.RoleInbox)
	seed("junk", "Junk", "2026-03-05T09:00:00Z", mail.RoleJunk)

	page, err := s.MessageList(ctx, MessageListQuery{Role: mail.RoleInbox, Limit: 10, Counts: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{d, c, b, a}
	if got := messageListIDs(page); len(got) != len(want) {
		t.Fatalf("role page rows %v, want %v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("role page rows %v, want %v", got, want)
			}
		}
	}
	if page.Total != 4 || page.Counts[mail.RoleInbox] != 4 || page.Counts[mail.RoleJunk] != 1 {
		t.Fatalf("role page total=%d counts=%v", page.Total, page.Counts)
	}

	// The keyset cursor crosses the folder boundary: page two starts where
	// the merged page one stopped, and the junk folder never enters the
	// inbox population.
	first, err := s.MessageList(ctx, MessageListQuery{Role: mail.RoleInbox, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := messageListIDs(first); len(got) != 2 || got[0] != d || got[1] != c || !first.HasMore {
		t.Fatalf("first role page %v more=%v", got, first.HasMore)
	}
	before, err := ParseCursor(first.NextBefore)
	if err != nil {
		t.Fatal(err)
	}
	if before.Role != mail.RoleInbox {
		t.Fatalf("role cursor lost its role: %+v", before)
	}
	second, err := s.MessageList(ctx, MessageListQuery{Role: mail.RoleInbox, Before: before, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := messageListIDs(second); len(got) != 2 || got[0] != b || got[1] != a || second.HasMore {
		t.Fatalf("second role page %v more=%v", got, second.HasMore)
	}
}

// TestMessageListAggregatesReadFolderRange pins the total and the role counts
// to the covering folder index: they aggregate one folder's message range,
// never scan messages and never probe each owner verdict by row id.
func TestMessageListAggregatesReadFolderRange(t *testing.T) {
	s, _ := open(t, nil)
	messageListSeed(t, s, "inbox", nil)
	where := visibleSQL + " AND " + ownerContentWhere
	for name, query := range map[string]string{
		"counts": `SELECT f.role, COUNT(*)` + messageListFromSQL + ` WHERE ` + where + ` GROUP BY f.role`,
		"total":  `SELECT COUNT(*)` + messageListFromSQL + ` WHERE ` + where,
	} {
		plan := priorityQueryPlan(t, s, query)
		if !strings.Contains(plan, "SEARCH m USING COVERING INDEX messages_folder_run (folder_id=?)") {
			t.Fatalf("%s does not read the covering folder index:\n%s", name, plan)
		}
		if strings.Contains(plan, "SCAN m") {
			t.Fatalf("%s scans messages:\n%s", name, plan)
		}
	}
}
