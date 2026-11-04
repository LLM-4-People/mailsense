package store

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

// threadTestMessage builds one synthetic member of conversation conv. Its
// scope verdict is written separately (seedThreadState) or derived from the
// store's owner identity fallback.
func threadTestMessage(id int64, storeID, conversationID, when, sender, name string) mail.Message {
	m := mailtest.Message("src", name+string(rune('a'+id)))
	m.StoreID = storeID
	m.Subject = "Thread " + name
	m.Body = "A substantive message about the project."
	m.ConversationID = conversationID
	m.SenderName, m.SenderAddress = name, sender
	m.SentOn, m.ReceivedAt = when, when
	m.Recipients = []mail.Recipient{{Kind: mail.To, Name: "Owner", Address: mailtest.Owner},
		{Kind: mail.To, Name: "Colleague", Address: "colleague@example.test"}}
	m.AddressedToMe = mail.AddressedTo
	return m
}

// sentThreadMessage is a thread member in the sent folder.
func sentThreadMessage(id int64, storeID, conversationID, when, sender, name string) mail.Message {
	m := threadTestMessage(id, storeID, conversationID, when, sender, name)
	m.FolderRole, m.FolderPath = mail.RoleSent, "Sent"
	return m
}

// seedThreadState stores one message, writes its scope verdict when it has
// one, and returns its row id.
func seedThreadState(t *testing.T, s *Store, m mail.Message, verdict scope.Verdict, reason scope.Reason) int64 {
	t.Helper()
	id := answerMessage(t, s, m)
	if verdict != "" {
		mailQueryVerdict(t, s, id, verdict, reason)
	}
	return id
}

func TestThreadStatesColleagueReplyAcrossStores(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	in := threadTestMessage(1, "store-1", "C1", "2026-11-03T08:00:00Z", "client@example.test", "Client")
	reply := sentThreadMessage(2, "store-2", "C1", "2026-11-03T09:00:00Z", "colleague@example.test", "Colleague")
	inID := seedThreadState(t, s, in, scope.Owner, scope.ReasonRecipientMatch)
	seedThreadState(t, s, reply, scope.Shared, scope.ReasonSentByOther)

	states, err := s.ThreadStates(ctx, []string{"C1"}, map[int64]threadintel.Signal{inID: {ReplyNeeded: true}})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := states["C1"]
	if !ok {
		t.Fatalf("no state for C1: %+v", states)
	}
	if got.State != threadintel.AnsweredByOther || got.NeedsReply() || got.ActorName != "Colleague" ||
		got.ReplyToMessageID != inID {
		t.Fatalf("state %+v", got)
	}
	if got.OwnerActorName != "" || got.OwnerActorAddress != "" {
		t.Fatalf("a shared reply leaked an owner-facing actor: %+v", got)
	}
}

func TestThreadStatesNoReplyAndPartialVisibility(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	in := threadTestMessage(1, "store-1", "C1", "2026-11-03T08:00:00Z", "client@example.test", "Client")
	inID := seedThreadState(t, s, in, scope.Owner, scope.ReasonRecipientMatch)
	states, err := s.ThreadStates(ctx, []string{"C1"}, map[int64]threadintel.Signal{inID: {ReplyNeeded: true}})
	if err != nil {
		t.Fatal(err)
	}
	if got := states["C1"]; got.State != threadintel.AwaitingOwner || !got.NeedsReply() ||
		got.OwnerActorName != "Client" || got.OwnerActorAddress != "client@example.test" {
		t.Fatalf("complete visibility state %+v", got)
	}

	// A shared mailbox exists but is not picked: the reply may be outside
	// the visible evidence, so the resolver must not claim the owner owes.
	if err := s.SaveMailboxes(ctx, "src", []source.Mailbox{{SourceID: "src", StoreID: "store-3",
		Key: source.StoreKey("src", "store-3"), Name: "Shared", Kind: mail.StoreShared}}); err != nil {
		t.Fatal(err)
	}
	states, err = s.ThreadStates(ctx, []string{"C1"}, map[int64]threadintel.Signal{inID: {ReplyNeeded: true}})
	if err != nil {
		t.Fatal(err)
	}
	if got := states["C1"]; got.State != threadintel.Unknown || got.Reason != threadintel.ReasonVisibilityGap {
		t.Fatalf("partial visibility state %+v", got)
	}
}

// TestThreadStatesSharedStoreActorIsNotOwnerFacing reproduces the leak the
// digest cannot see: a picked shared store marks its own mailbox owner as
// verdict=owner. The state still knows the colleague replied, but the
// content-facing actor must stay empty.
func TestThreadStatesSharedStoreActorIsNotOwnerFacing(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	if _, err := s.UpsertFolder(ctx, mail.Folder{SourceID: "src", StoreID: "store-2",
		StoreName: "Shared", Path: "Sent", Role: mail.RoleSent, StoreKind: mail.StoreShared}); err != nil {
		t.Fatal(err)
	}
	in := threadTestMessage(1, "store-1", "C1", "2026-11-03T08:00:00Z", "client@example.test", "Client")
	reply := sentThreadMessage(2, "store-2", "C1", "2026-11-03T09:00:00Z", "colleague@example.test", "Colleague")
	inID := seedThreadState(t, s, in, scope.Owner, scope.ReasonRecipientMatch)
	seedThreadState(t, s, reply, scope.Owner, scope.ReasonSentByOwner)

	states, err := s.ThreadStates(ctx, []string{"C1"}, map[int64]threadintel.Signal{inID: {ReplyNeeded: true}})
	if err != nil {
		t.Fatal(err)
	}
	got := states["C1"]
	if got.State != threadintel.AnsweredByOther || got.ActorName != "Colleague" {
		t.Fatalf("state %+v", got)
	}
	if got.OwnerActorName != "" || got.OwnerActorAddress != "" {
		t.Fatalf("shared-store owner verdict leaked an owner-facing actor: %+v", got)
	}
}

// TestThreadVisibilityCountsOnlyActivePickedFolders: a shared/delegate
// mailbox is fully visible only when one of its folders is both active and
// picked, the one mailbox predicate of every read over the user's mail
// (visibleSQL). A picked folder that discovery deactivated is missing
// evidence, not proof that no colleague replied.
func TestThreadVisibilityCountsOnlyActivePickedFolders(t *testing.T) {
	type folderState struct {
		picked bool
		active bool
	}
	type mailboxState struct {
		store   string
		kind    mail.StoreKind
		folders []folderState
	}
	cases := []struct {
		name      string
		mailboxes []mailboxState
		want      threadintel.Visibility
	}{
		{
			name:      "picked and active folder",
			mailboxes: []mailboxState{{store: "store-shared", kind: mail.StoreShared, folders: []folderState{{picked: true, active: true}}}},
			want:      threadintel.Complete,
		},
		{
			name:      "picked but inactive folder",
			mailboxes: []mailboxState{{store: "store-shared", kind: mail.StoreShared, folders: []folderState{{picked: true, active: false}}}},
			want:      threadintel.Partial,
		},
		{
			name:      "active but unpicked folder",
			mailboxes: []mailboxState{{store: "store-shared", kind: mail.StoreShared, folders: []folderState{{picked: false, active: true}}}},
			want:      threadintel.Partial,
		},
		{
			name:      "no folder",
			mailboxes: []mailboxState{{store: "store-shared", kind: mail.StoreShared}},
			want:      threadintel.Partial,
		},
		{
			name: "one of two mailboxes partial",
			mailboxes: []mailboxState{
				{store: "store-shared", kind: mail.StoreShared, folders: []folderState{{picked: true, active: true}}},
				{store: "store-delegate", kind: mail.StoreDelegate, folders: []folderState{{picked: true, active: false}}},
			},
			want: threadintel.Partial,
		},
		{
			name:      "primary only",
			mailboxes: []mailboxState{{store: "store-primary", kind: mail.StorePrimary, folders: []folderState{{picked: true, active: true}}}},
			want:      threadintel.Complete,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			selected := mailtest.Picked([]string{"src"})
			var boxes []source.Mailbox
			for _, mb := range tc.mailboxes {
				boxes = append(boxes, source.Mailbox{SourceID: "src", StoreID: mb.store,
					Key: source.StoreKey("src", mb.store), Name: mb.store, Kind: mb.kind})
				for _, f := range mb.folders {
					if f.picked {
						selected = append(selected, source.StoreKey("src", mb.store))
					}
				}
			}
			s, _ := open(t, map[string]any{source.SettingSelected: selected})
			ctx := t.Context()
			if err := s.SaveMailboxes(ctx, "src", boxes); err != nil {
				t.Fatal(err)
			}
			for _, mb := range tc.mailboxes {
				for _, f := range mb.folders {
					id, err := s.UpsertFolder(ctx, mail.Folder{SourceID: "src", StoreID: mb.store, StoreName: mb.store,
						Path: "Inbox", Role: mail.RoleInbox, StoreKind: mb.kind})
					if err != nil {
						t.Fatal(err)
					}
					if !f.active {
						if err := s.SetFolderActive(ctx, id, false); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			got, err := s.threadVisibility(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("visibility = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestThreadStatesSingleToRecipientStillOwes proves the ManyTo scalar is
// "more than one To recipient", not "has any recipient": a one-recipient ask
// still becomes AwaitingOwner when shared evidence is incomplete.
func TestThreadStatesSingleToRecipientStillOwes(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	in := threadTestMessage(1, "store-1", "C1", "2026-11-03T08:00:00Z", "client@example.test", "Client")
	in.Recipients = []mail.Recipient{{Kind: mail.To, Name: "Owner", Address: mailtest.Owner}}
	inID := seedThreadState(t, s, in, scope.Owner, scope.ReasonRecipientMatch)

	// A shared mailbox exists but is not picked: the visibility is Partial.
	if err := s.SaveMailboxes(ctx, "src", []source.Mailbox{{SourceID: "src", StoreID: "store-3",
		Key: source.StoreKey("src", "store-3"), Name: "Shared", Kind: mail.StoreShared}}); err != nil {
		t.Fatal(err)
	}
	states, err := s.ThreadStates(ctx, []string{"C1"}, map[int64]threadintel.Signal{inID: {ReplyNeeded: true}})
	if err != nil {
		t.Fatal(err)
	}
	got := states["C1"]
	if got.State != threadintel.AwaitingOwner || got.Reason != threadintel.ReasonInboundLast {
		t.Fatalf("state %+v", got)
	}
}

// readThreadState reads the resolver's view of one row through the real
// thread-state select, so the test pins the two derived columns themselves.
func readThreadState(t *testing.T, s *Store, rowID int64) threadintel.Message {
	t.Helper()
	rows, err := s.db.QueryContext(t.Context(), threadStateSelect+threadStateFrom+` WHERE m.id = ?`, rowID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("no thread state for row %d: %v", rowID, rows.Err())
	}
	m, err := scanThreadState(rows, nil)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestThreadStatesSentOwnerFallbackUsesTheOwnerIdentity pins the conditional
// owner-identity probe: a sent row with no scope verdict is the owner's own
// reply when the store's identity matches its sender and stays unjudged when
// it does not.
func TestThreadStatesSentOwnerFallbackUsesTheOwnerIdentity(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	if err := s.SaveReported(ctx, StoreRef{SourceID: "src", StoreID: "store-1"},
		[]string{"owner@example.test"}); err != nil {
		t.Fatal(err)
	}
	ownerReply := sentThreadMessage(2, "store-1", "C1", "2026-11-03T09:00:00Z", "owner@example.test", "Owner")
	ownerReplyID := seedThreadState(t, s, ownerReply, "", "")
	colleagueReply := sentThreadMessage(4, "store-1", "C2", "2026-11-03T09:00:00Z", "colleague@example.test", "Colleague")
	colleagueReplyID := seedThreadState(t, s, colleagueReply, "", "")

	if got := readThreadState(t, s, ownerReplyID); got.Verdict != scope.Owner || got.Reason != scope.ReasonSentByOwner {
		t.Fatalf("owner sent fallback: verdict %q reason %q", got.Verdict, got.Reason)
	}
	if got := readThreadState(t, s, colleagueReplyID); got.Verdict != "" || got.Reason != "" {
		t.Fatalf("a non-owner sender took the owner fallback: verdict %q reason %q", got.Verdict, got.Reason)
	}
}

func TestThreadStatesIgnoresUnpickedReply(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	in := threadTestMessage(1, "store-1", "C1", "2026-11-03T08:00:00Z", "client@example.test", "Client")
	reply := sentThreadMessage(2, "store-3", "C1", "2026-11-03T09:00:00Z", "colleague@example.test", "Colleague")
	inID := seedThreadState(t, s, in, scope.Owner, scope.ReasonRecipientMatch)
	seedThreadState(t, s, reply, "", "")
	states, err := s.ThreadStates(ctx, []string{"C1"}, map[int64]threadintel.Signal{inID: {ReplyNeeded: true}})
	if err != nil {
		t.Fatal(err)
	}
	if got := states["C1"]; got.State == threadintel.AnsweredByOther {
		t.Fatalf("an unpicked store's reply was treated as visible: %+v", got)
	}
}

// Deferred evidence must not change the state: the newest auto-reply and a
// trivial owner acknowledgement are fetched only to be skipped, and the
// resolver still reaches the older client request.
func TestThreadStatesDeferredEvidenceKeepsTheOldestRequest(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	const conv = "C-deferred"
	oldest := threadTestMessage(1, "store-1", conv, "2026-11-03T08:00:00Z", "client@example.test", "Client")
	oldest.Body = "Could you review the plan by Friday?"
	middle := sentThreadMessage(2, "store-1", conv, "2026-11-03T09:00:00Z", "owner@example.test", "Owner")
	middle.Body, middle.HasAttachments = "Thanks", false
	newest := threadTestMessage(3, "store-1", conv, "2026-11-03T10:00:00Z", "auto@example.test", "Auto")
	newest.Body, newest.HasAttachments = "I am away until Monday.", false
	newest.MessageClass = "IPM.Note.Rules.OofTemplate.Microsoft"
	oldestID := seedThreadState(t, s, oldest, scope.Owner, scope.ReasonRecipientMatch)
	seedThreadState(t, s, middle, scope.Owner, scope.ReasonSentByOwner)
	seedThreadState(t, s, newest, scope.Owner, scope.ReasonRecipientMatch)
	states, err := s.ThreadStates(ctx, []string{conv}, map[int64]threadintel.Signal{oldestID: {ReplyNeeded: true}})
	if err != nil {
		t.Fatal(err)
	}
	got := states[conv]
	if got.State != threadintel.AwaitingOwner || got.Reason != threadintel.ReasonInboundLast {
		t.Fatalf("deferred evidence changed the state: %+v", got)
	}
}

// TestThreadStatesBatchParity proves the evidence batch is only an execution
// detail: one fixture must produce the exact same assessments whether a chunk
// holds one conversation, five, or all of them.
func TestThreadStatesBatchParity(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	signals := map[int64]threadintel.Signal{}
	seed := func(m mail.Message, verdict scope.Verdict, reason scope.Reason, signal *threadintel.Signal) {
		id := seedThreadState(t, s, m, verdict, reason)
		if signal != nil {
			signals[id] = *signal
		}
	}
	ask := threadintel.Signal{ReplyNeeded: true, IsQuestion: true, AsksFor: "review"}

	// Deferred evidence: auto-reply, owner reply, then the original ask.
	seed(threadTestMessage(1, "store-1", "C-parity-deferred", "2026-11-03T08:00:00Z", "client@example.test", "Client"), scope.Owner, scope.ReasonRecipientMatch, &ask)
	seed(sentThreadMessage(2, "store-1", "C-parity-deferred", "2026-11-03T09:00:00Z", "owner@example.test", "Owner"), scope.Owner, scope.ReasonSentByOwner, nil)
	auto := threadTestMessage(3, "store-1", "C-parity-deferred", "2026-11-03T10:00:00Z", "auto@example.test", "Auto")
	auto.MessageClass = "IPM.Note.Rules.OofTemplate.Microsoft"
	seed(auto, scope.Owner, scope.ReasonRecipientMatch, nil)

	// A trivial acknowledgement is deferred evidence too.
	seed(threadTestMessage(4, "store-1", "C-parity-ack", "2026-11-03T08:00:00Z", "client@example.test", "Client"), scope.Owner, scope.ReasonRecipientMatch, &ask)
	ack := sentThreadMessage(5, "store-1", "C-parity-ack", "2026-11-03T09:00:00Z", "owner@example.test", "Owner")
	ack.Body, ack.HasAttachments = "Thanks", false
	seed(ack, scope.Owner, scope.ReasonSentByOwner, nil)

	// A colleague reply from a second picked store.
	seed(threadTestMessage(6, "store-1", "C-parity-shared", "2026-11-03T08:00:00Z", "client@example.test", "Client"), scope.Owner, scope.ReasonRecipientMatch, &ask)
	seed(sentThreadMessage(7, "store-2", "C-parity-shared", "2026-11-03T09:00:00Z", "colleague@example.test", "Colleague"), scope.Shared, scope.ReasonSentByOther, nil)

	// A reply in an unpicked store is not visible evidence.
	seed(threadTestMessage(8, "store-1", "C-parity-unpicked", "2026-11-03T08:00:00Z", "client@example.test", "Client"), scope.Owner, scope.ReasonRecipientMatch, &ask)
	seed(sentThreadMessage(9, "store-3", "C-parity-unpicked", "2026-11-03T09:00:00Z", "colleague@example.test", "Colleague"), "", "", nil)

	// An empty newest body must fall through to the older ask.
	seed(threadTestMessage(10, "store-1", "C-parity-empty", "2026-11-03T08:00:00Z", "client@example.test", "Client"), scope.Owner, scope.ReasonRecipientMatch, &ask)
	empty := threadTestMessage(11, "store-1", "C-parity-empty", "2026-11-03T09:00:00Z", "client@example.test", "Client")
	empty.Body, empty.HasAttachments = "", false
	seed(empty, scope.Owner, scope.ReasonRecipientMatch, nil)

	// Equal instants order by row id in the resolver, so chunking must not
	// disturb the tie-break.
	seed(threadTestMessage(12, "store-1", "C-parity-tie", "2026-11-03T08:00:00Z", "first@example.test", "First"), scope.Owner, scope.ReasonRecipientMatch, &ask)
	seed(threadTestMessage(13, "store-1", "C-parity-tie", "2026-11-03T08:00:00Z", "second@example.test", "Second"), scope.Owner, scope.ReasonRecipientMatch, nil)

	seed(threadTestMessage(14, "store-1", "C-parity-single", "2026-11-03T08:00:00Z", "client@example.test", "Client"), scope.Owner, scope.ReasonRecipientMatch, &ask)

	ids := []string{
		"C-parity-deferred", "C-parity-ack", "C-parity-shared", "C-parity-unpicked",
		"C-parity-empty", "C-parity-tie", "C-parity-single", "C-parity-missing",
	}
	want, err := s.threadStates(ctx, ids, signals, len(ids), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, batch := range []int{1, 5, len(ids)} {
		t.Run(fmt.Sprintf("batch=%d", batch), func(t *testing.T) {
			got, err := s.threadStates(ctx, ids, signals, batch, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("batch %d changed the assessments:\n got %#v\nwant %#v", batch, got, want)
			}
		})
	}
	public, err := s.ThreadStates(ctx, ids, signals)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(public, want) {
		t.Fatalf("public ThreadStates changed the assessments:\n got %#v\nwant %#v", public, want)
	}
}

// TestThreadStatesEvidencePeak proves the bound: 620 conversations (more than
// one 500-conversation chunk) with multi-KB bodies and a deferred auto-reply
// must never keep more than one chunk's frontier rows resident. A smaller
// injected batch must tighten the bound without changing the assessments.
func TestThreadStatesEvidencePeak(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	const conversations = 620
	msgs := make([]mail.Message, 0, conversations*2)
	for i := 0; i < conversations; i++ {
		id := int64(i)
		conv := fmt.Sprintf("C-peak-%03d", i)
		base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute)
		in := threadTestMessage(2*id+1, "store-1", conv, base.Format(mail.TimeLayout), "client@example.test", "Client")
		in.Body = strings.Repeat("Could you review the plan before Friday? ", 160)
		auto := threadTestMessage(2*id+2, "store-1", conv, base.Add(time.Minute).Format(mail.TimeLayout), "auto@example.test", "Auto")
		auto.Body = strings.Repeat("I am away until Monday. ", 240)
		auto.MessageClass = "IPM.Note.Rules.OofTemplate.Microsoft"
		msgs = append(msgs, in, auto)
	}
	if err := s.UpsertMessages(ctx, 0, msgs); err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM messages WHERE source_id = 'src' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var verdicts []VerdictRow
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		verdicts = append(verdicts, VerdictRow{RowID: id, Verdict: scope.Owner, Reason: scope.ReasonRecipientMatch})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatal(err)
	}
	_ = rows.Close()
	if err := s.WriteVerdicts(ctx, "test", verdicts); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, conversations)
	for i := range ids {
		ids[i] = fmt.Sprintf("C-peak-%03d", i)
	}

	var peak int
	states, err := s.threadStates(ctx, ids, nil, ThreadStatesBatch, &peak)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != len(ids) {
		t.Fatalf("resolved %d of %d conversations", len(states), len(ids))
	}
	if peak == 0 || peak > ThreadStatesBatch {
		t.Fatalf("peak evidence rows %d, want 1..%d", peak, ThreadStatesBatch)
	}

	var smallPeak int
	small, err := s.threadStates(ctx, ids, nil, 7, &smallPeak)
	if err != nil {
		t.Fatal(err)
	}
	if smallPeak == 0 || smallPeak > 7 {
		t.Fatalf("small-batch peak evidence rows %d, want 1..7", smallPeak)
	}
	if !reflect.DeepEqual(small, states) {
		t.Fatalf("the injected batch changed the assessments")
	}
	t.Logf("evidence peak: batch=%d -> %d rows, batch=7 -> %d rows", ThreadStatesBatch, peak, smallPeak)
}
