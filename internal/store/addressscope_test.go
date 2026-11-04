package store

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
)

// seedLatestThreadWorld seeds the mixed fixture of the latest-thread read:
// received and sent mail for the same people, an address whose newest message
// is sent, one whose newest is received, equal-instant ties on both insertion
// orders, and hidden, shared, draft and unthreaded mail that must never win.
func seedLatestThreadWorld(t *testing.T) (*Store, map[string]int64) {
	t.Helper()
	s, _ := open(t, nil)
	if err := s.SaveReported(t.Context(), StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	recv := func(id, from, when, conv string) mail.Message {
		m := mailtest.Message("src", id)
		m.SenderName, m.SenderAddress = "Counterpart", from
		m.ReceivedAt, m.SentOn = when, when
		m.Subject = "Received " + id
		m.ConversationID = conv
		return m
	}
	toAddr := func(id, address, when string) mail.Message {
		m := sent(id, when, mail.Recipient{Kind: mail.To, Name: "Counterpart", Address: address})
		m.Subject = "Sent " + id
		return m
	}
	messages := []mail.Message{
		recv("r1", "ada@example.com", "2026-10-01T10:00:00Z", "C-r1"),
		toAddr("s1", "ada@example.com", "2026-10-02T10:00:00Z"),
		recv("r2", "ada@example.com", "2026-10-04T10:00:00Z", "C-r2"), // newest for ada
		toAddr("s2", "grace@example.com", "2026-10-05T10:00:00Z"),     // newest for grace
		recv("r3", "linus@example.com", "2026-10-06T10:00:00Z", "C-r3"),
		toAddr("s3", "linus@example.com", "2026-10-06T10:00:00Z"), // equal instant, later row
		toAddr("s4", "zoe@example.com", "2026-10-07T10:00:00Z"),
		recv("r5", "zoe@example.com", "2026-10-07T10:00:00Z", "C-r5"), // equal instant, later row
	}
	hidden := toAddr("s-hidden", "ada@example.com", "2026-10-08T10:00:00Z")
	hidden.StoreID = "store-3" // discovered but unpicked
	messages = append(messages, hidden)
	shared := recv("r-shared", "ada@example.com", "2026-10-09T10:00:00Z", "C-shared")
	shared.StoreID = "store-2"
	messages = append(messages, shared)
	draft := toAddr("draft", "ada@example.com", "2026-10-10T10:00:00Z")
	draft.FolderRole, draft.FolderPath = mail.RoleDrafts, "Drafts"
	messages = append(messages, draft)
	unthreaded := toAddr("s-no-conv", "ada@example.com", "2026-10-11T10:00:00Z")
	unthreaded.ConversationID = ""
	messages = append(messages, unthreaded)
	unpicked := recv("r-unpicked", "ada@example.com", "2026-10-12T10:00:00Z", "C-unpicked")
	unpicked.StoreID = "store-3"
	messages = append(messages, unpicked)
	// A recipient row written before the normalized column carried a value:
	// the exact-address branch keeps the raw fallback of the old predicate.
	legacy := toAddr("s-legacy", "legacy@example.com", "2026-10-13T10:00:00Z")
	messages = append(messages, legacy)
	// Unicode case folding must match Go: SQLite's lower() is ASCII-only.
	accented := recv("r-accent", "ÉMILE.DUPONT@EXEMPLE.CA", "2026-10-14T10:00:00Z", "C-accent")
	messages = append(messages, accented)
	if _, err := s.UpsertFolder(t.Context(), mail.Folder{SourceID: "src", StoreID: "store-2",
		StoreName: "Shared", Path: "Inbox", Role: mail.RoleInbox, StoreKind: mail.StoreShared}); err != nil {
		t.Fatal(err)
	}
	ids := seedMsgs(t, s, messages...)
	if _, err := s.db.ExecContext(t.Context(), "UPDATE recipients SET address_norm = '' WHERE message_id = ? AND address = ?",
		ids["s-legacy"], "legacy@example.com"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"s1", "s2", "s3", "s4", "s-hidden", "draft", "s-no-conv", "s-legacy"} {
		mailQueryVerdict(t, s, ids[id], scope.Owner, scope.ReasonSentByOwner)
	}
	for _, id := range []string{"r1", "r2", "r3", "r5", "r-shared", "r-unpicked", "r-accent"} {
		mailQueryVerdict(t, s, ids[id], scope.Owner, scope.ReasonRecipientMatch)
	}
	return s, ids
}

// latestThreadLegacy is the old statement shape: one OR over the recipient
// and sender halves, ordered once over the owner population. It is the
// parity oracle of the split read.
func latestThreadLegacy(t *testing.T, s *Store, addresses []string) (string, int64) {
	t.Helper()
	addresses = normalizeScopeAddresses(addresses)
	if len(addresses) == 0 {
		return "", 0
	}
	vis, err := s.visible(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	predicate, args := exactCounterpartWhere(addresses)
	var conversationID string
	var messageID int64
	err = s.db.QueryRowContext(t.Context(), `SELECT m.conversation_id, m.id
		FROM messages m
		JOIN folders f ON f.id = m.folder_id
		JOIN scope_verdicts v ON v.message_id = m.id
		WHERE `+strings.Join([]string{vis, ownerContentWhere, threadContentRoleWhere, predicate,
		"m.conversation_id <> ''"}, " AND ")+`
		ORDER BY `+instantSQL+` DESC, m.id DESC LIMIT 1`, args...).Scan(&conversationID, &messageID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return conversationID, messageID
}

func TestAddressNormalizationSQLMatchesGo(t *testing.T) {
	s, _ := open(t, nil)
	cases := []string{
		"  ÉMILE.DUPONT@EXEMPLE.CA  ",
		"Info@X.TR",
		"Amélie.Durand@Example.com",
		"ßeta@example.org",
		"",
	}
	for _, in := range cases {
		var got string
		if err := s.db.QueryRowContext(t.Context(), "SELECT mailsense_lower(?)", in).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if want := mail.NormalizeAddress(in); got != want {
			t.Errorf("mailsense_lower(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAddressPredicatesUseTheCanonicalSQLNormalizer(t *testing.T) {
	for name, sql := range map[string]string{
		"recipient":    recipientNormSQL,
		"identity":     identityNormSQL,
		"representing": representingNormSQL,
	} {
		if !strings.Contains(sql, "mailsense_lower") {
			t.Errorf("%s predicate does not use mailsense_lower: %s", name, sql)
		}
		if strings.Contains(sql, "lower(trim") {
			t.Errorf("%s predicate still uses ASCII-only lower(trim()): %s", name, sql)
		}
	}
	if strings.Contains(latestSentQuery(visibleSQL), "lower(trim") {
		t.Error("the sent fallback still uses ASCII-only lower(trim())")
	}
}

func TestLatestThreadForAddressesKeepsTheNewestCounterpart(t *testing.T) {
	s, ids := seedLatestThreadWorld(t)
	cases := []struct {
		name      string
		addresses []string
		conv      string
		id        int64
	}{
		{"received newest over sent", []string{"ada@example.com"}, "C-r2", ids["r2"]},
		{"sent only", []string{"grace@example.com"}, "C-s2", ids["s2"]},
		{"equal instant later sent row", []string{"linus@example.com"}, "C-s3", ids["s3"]},
		{"equal instant later received row", []string{"zoe@example.com"}, "C-r5", ids["r5"]},
		{"normalized aliases and both branches", []string{"Ada@Example.com", " grace@example.com "}, "C-s2", ids["s2"]},
		{"unknown address", []string{"nobody@example.com"}, "", 0},
		{"no addresses", nil, "", 0},
		{"legacy unnormalized recipient row", []string{"Legacy@Example.com"}, "C-s-legacy", ids["s-legacy"]},
		{"accented uppercase counterpart", []string{"émile.dupont@exemple.ca"}, "C-accent", ids["r-accent"]},
		{"accented uppercase input", []string{"ÉMILE.DUPONT@EXEMPLE.CA"}, "C-accent", ids["r-accent"]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conv, id, err := s.LatestThreadForAddresses(t.Context(), tc.addresses)
			if err != nil {
				t.Fatal(err)
			}
			if conv != tc.conv || id != tc.id {
				t.Fatalf("latest = (%q, %d), want (%q, %d)", conv, id, tc.conv, tc.id)
			}
			wantConv, wantID := latestThreadLegacy(t, s, tc.addresses)
			if conv != wantConv || id != wantID {
				t.Fatalf("split read (%q, %d) differs from the counterpart scan (%q, %d)", conv, id, wantConv, wantID)
			}
		})
	}
}

// Both branches read their own index: the received half is a sender-key
// range, the sent half looks the exact address up in recipients_address and
// the message page stays on the instant order. Neither branch sorts.
func TestLatestThreadPinsBothIndexes(t *testing.T) {
	s, _ := seedLatestThreadWorld(t)
	addresses := jsonList([]string{"ada@example.com"})
	received := priorityQueryPlan(t, s, latestReceivedQuery(visibleSQL), "ada@example.com")
	if !strings.Contains(received, "messages_sender_norm (sender_norm=?)") || strings.Contains(received, "TEMP B-TREE") {
		t.Fatalf("the received branch does not read the sender index in page order:\n%s", received)
	}
	sent := priorityQueryPlan(t, s, latestSentQuery(visibleSQL), addresses, addresses)
	if !strings.Contains(sent, "recipients_address (address_norm=?)") ||
		!strings.Contains(sent, "messages_instant") || strings.Contains(sent, "TEMP B-TREE") {
		t.Fatalf("the sent branch does not read the recipient and instant indexes in page order:\n%s", sent)
	}
}
