package store

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

// packedConversations is the fixture size of the evidence parity gate: more
// than one 500-conversation evidence chunk would be, with every role, verdict,
// store kind, protocol kind and tie-order case mixed in.
const packedConversations = 600

// seedPackedThreads stores one deterministic, packed mailbox: 600
// conversations of 1..6 messages, mixing inbound asks, owner replies,
// colleague replies from a picked shared store, an unpicked store, auto
// replies, read receipts, trivial bodies, empty bodies, attachments and equal
// instants. It returns the conversation ids and the classifier signals keyed
// by row id.
func seedPackedThreads(t *testing.T, s *Store) ([]string, map[int64]threadintel.Signal) {
	t.Helper()
	ctx := t.Context()
	// The owner identity makes the sent-owner fallback deterministic for the
	// rows deliberately left without a scope verdict.
	if err := s.SaveReported(ctx, StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	bodies := []string{
		"Could you review the attached plan by Friday?",
		"Thanks",
		"",
		strings.Repeat("This is a substantive sentence about the plan. ", 60),
		"ok",
		"bien reçu",
		"I am away until Monday.",
	}
	var msgs []mail.Message
	rows := make(map[string]mail.Message)
	for c := range packedConversations {
		conv := fmt.Sprintf("C-packed-%03d", c)
		base := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC).Add(time.Duration(c) * time.Hour)
		n := 1 + c%6
		for j := range n {
			when := base.Add(time.Duration(j) * time.Minute)
			if c%11 == 0 {
				// Equal instants force the row-id tie-break in the resolver.
				when = base
			}
			name := fmt.Sprintf("P%03dM%d", c, j)
			var m mail.Message
			switch j % 5 {
			case 0:
				m = threadTestMessage(int64(j), "store-1", conv, when.Format(mail.TimeLayout), "client@example.test", name)
			case 1:
				m = sentThreadMessage(int64(j), "store-1", conv, when.Format(mail.TimeLayout), "owner@example.test", name)
			case 2:
				m = sentThreadMessage(int64(j), "store-2", conv, when.Format(mail.TimeLayout), "colleague@example.test", name)
			case 3, 4:
				m = sentThreadMessage(int64(j), "store-1", conv, when.Format(mail.TimeLayout), "auto@example.test", name)
			}
			m.Body = bodies[(c+j)%len(bodies)]
			// mailtest.Message carries reply headers and an attachment by
			// default; the packed fixture sets both explicitly.
			m.HasAttachments, m.InReplyTo, m.References, m.LastVerbExecuted, m.Headers = false, "", "", mail.Int(0), ""
			switch j % 5 {
			case 1:
				m.HasAttachments = c%7 == 0
				if c%5 == 0 {
					m.InReplyTo = "<packed@example.test>"
				}
			case 3:
				m.Body = "I am away until Monday."
				m.MessageClass = "IPM.Note.Rules.OofTemplate.Microsoft"
			case 4:
				m.Body, m.MessageClass = "", "IPM.Note.ReadReceipt"
			}
			msgs = append(msgs, m)
			rows[m.StoreID+"\x00"+m.ID] = m
		}
		// An unpicked store's copy exists but is invisible evidence.
		if c%17 == 0 {
			when := base.Add(time.Duration(n) * time.Minute)
			m := sentThreadMessage(int64(0), "store-3", conv, when.Format(mail.TimeLayout),
				"colleague@example.test", fmt.Sprintf("P%03dU", c))
			m.Body = "A reply in an unpicked store."
			m.HasAttachments, m.InReplyTo, m.References, m.LastVerbExecuted, m.Headers = false, "", "", mail.Int(0), ""
			msgs = append(msgs, m)
			rows[m.StoreID+"\x00"+m.ID] = m
		}
	}
	if err := s.UpsertMessages(ctx, 0, msgs); err != nil {
		t.Fatal(err)
	}
	var verdicts []VerdictRow
	signals := make(map[int64]threadintel.Signal)
	selectRows, err := s.db.QueryContext(ctx, `SELECT id, store_id, msg_id FROM messages ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer selectRows.Close()
	for selectRows.Next() {
		var id int64
		var storeID, msgID string
		if err := selectRows.Scan(&id, &storeID, &msgID); err != nil {
			t.Fatal(err)
		}
		m := rows[storeID+"\x00"+msgID]
		switch {
		case storeID == "store-2":
			verdicts = append(verdicts, VerdictRow{RowID: id, Verdict: scope.Shared, Reason: scope.ReasonSentByOther})
		case storeID == "store-3":
			// Unpicked: no verdict, and invisible.
		case m.FolderRole == mail.RoleSent && id%5 == 0:
			// Deliberately unjudged: the owner identity probe decides it.
		case m.FolderRole == mail.RoleSent:
			verdicts = append(verdicts, VerdictRow{RowID: id, Verdict: scope.Owner, Reason: scope.ReasonSentByOwner})
		default:
			verdicts = append(verdicts, VerdictRow{RowID: id, Verdict: scope.Owner, Reason: scope.ReasonRecipientMatch})
		}
		if storeID == "store-1" {
			signals[id] = threadintel.Signal{
				ReplyNeeded: id%2 == 0, IsQuestion: id%3 == 0,
				AsksFor:   []string{"", "answer_question", "information_document", "task_work"}[id%4],
				Topic:     []string{"", "project", "billing"}[id%3],
				Automated: id%29 == 0,
			}
		}
	}
	if err := selectRows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteVerdicts(ctx, "test", verdicts); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, packedConversations)
	for i := range ids {
		ids[i] = fmt.Sprintf("C-packed-%03d", i)
	}
	return ids, signals
}

// referenceThreadState is the pre-optimization execution: it walks the
// deferred evidence with the stateless frontier (re-sorting and re-parsing on
// every round) and resolves the fully loaded thread with the stateless
// resolver. It is the oracle the Thread-based store path must match.
func referenceThreadState(t *testing.T, s *Store, conversationID string, signals map[int64]threadintel.Signal, visibility threadintel.Visibility) threadintel.Assessment {
	t.Helper()
	ctx := t.Context()
	vis, err := s.visible(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.QueryContext(ctx, threadStateSelect+threadStateFrom+`
		WHERE `+vis+`
			AND m.conversation_id = ?
			AND `+threadContentRoleWhere, conversationID)
	if err != nil {
		t.Fatal(err)
	}
	var msgs []threadintel.Message
	var rowIDs []int64
	for rows.Next() {
		m, err := scanThreadState(rows, signals)
		if err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		msgs = append(msgs, m)
		rowIDs = append(rowIDs, m.RowID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatal(err)
	}
	_ = rows.Close()
	evidence, err := loadThreadEvidence(ctx, s.db, rowIDs)
	if err != nil {
		t.Fatal(err)
	}
	loaded := map[int64]struct{}{}
	opts := threadintel.Options{Visibility: visibility}
	for {
		id, ok := threadintel.EvidenceFrontier(msgs, loaded, opts)
		if !ok {
			break
		}
		for i := range msgs {
			if msgs[i].RowID == id {
				msgs[i].Body, msgs[i].Headers = evidence[id].body, evidence[id].headers
				break
			}
		}
		loaded[id] = struct{}{}
	}
	return threadintel.Resolve(msgs, opts)
}

// TestThreadStatesPackedMatchesStateless is the correctness gate: the
// optimized path (Thread memo + shared parsed-evidence cache) must produce
// the exact pre-optimization Assessment for every conversation of a packed
// mailbox, field by field.
func TestThreadStatesPackedMatchesStateless(t *testing.T) {
	s, _ := open(t, nil)
	ids, signals := seedPackedThreads(t, s)
	visibility, err := s.threadVisibility(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ThreadStates(t.Context(), ids, signals)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(ids) {
		t.Fatalf("resolved %d of %d conversations", len(got), len(ids))
	}
	for _, id := range ids {
		want := referenceThreadState(t, s, id, signals, visibility)
		if got[id] != want {
			t.Fatalf("%s:\n got %#v\nwant %#v", id, got[id], want)
		}
	}
}

// TestThreadStatesSecondBuildParsesNothing is the cross-build contract: a
// second build over unchanged mail and the same shared cache performs zero
// parsed-evidence computations, i.e. zero mail.ParseHeaders calls for the
// cached messages.
func TestThreadStatesSecondBuildParsesNothing(t *testing.T) {
	s, _ := open(t, nil)
	ids, signals := seedPackedThreads(t, s)
	cache := threadintel.SharedCache()
	cache.Reset()
	if _, err := s.ThreadStates(t.Context(), ids, signals); err != nil {
		t.Fatal(err)
	}
	first := cache.Stats()
	if first.Parses == 0 {
		t.Fatal("the first build parsed nothing")
	}
	if _, err := s.ThreadStates(t.Context(), ids, signals); err != nil {
		t.Fatal(err)
	}
	second := cache.Stats()
	if second.Parses != first.Parses {
		t.Fatalf("the unchanged second build parsed %d more messages (first %+v, second %+v)",
			second.Parses-first.Parses, first, second)
	}
	if second.Hits <= first.Hits {
		t.Fatalf("the second build took no cache hits (first %+v, second %+v)", first, second)
	}
}

// TestThreadStatesRevisionBumpRecomputes: a message rewrite bumps revision,
// so the next build must parse the new content and change the assessment.
func TestThreadStatesRevisionBumpRecomputes(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	cache := threadintel.SharedCache()
	cache.Reset()

	conv := "C-revision"
	ask := threadTestMessage(1, "store-1", conv, "2026-11-03T08:00:00Z", "client@example.test", "Client")
	ask.HasAttachments, ask.LastVerbExecuted = false, mail.Int(0)
	askID := seedThreadState(t, s, ask, scope.Owner, scope.ReasonRecipientMatch)
	reply := sentThreadMessage(2, "store-1", conv, "2026-11-03T09:00:00Z", "owner@example.test", "Owner")
	reply.Body, reply.HasAttachments, reply.InReplyTo, reply.References = "Thanks", false, "", ""
	reply.LastVerbExecuted = mail.Int(0)
	replyID := seedThreadState(t, s, reply, scope.Owner, scope.ReasonSentByOwner)
	// A fresh insert starts at revision zero; one rewrite makes the change
	// marker observable before the first build.
	if err := s.UpsertMessages(ctx, 0, []mail.Message{reply}); err != nil {
		t.Fatal(err)
	}

	before := readThreadState(t, s, replyID)
	if before.Revision == 0 {
		t.Fatal("the fixture did not store a revision")
	}
	signals := map[int64]threadintel.Signal{askID: {ReplyNeeded: true}}
	first, err := s.ThreadStates(ctx, []string{conv}, signals)
	if err != nil {
		t.Fatal(err)
	}
	if got := first[conv]; got.State != threadintel.AwaitingOwner || got.Reason != threadintel.ReasonInboundLast {
		t.Fatalf("trivial reply state %+v", got)
	}
	parses := cache.Stats().Parses

	reply.Body = "Done, the plan is attached."
	reply.InReplyTo = "<packed@example.test>"
	if err := s.UpsertMessages(ctx, 0, []mail.Message{reply}); err != nil {
		t.Fatal(err)
	}
	after := readThreadState(t, s, replyID)
	if after.Revision <= before.Revision {
		t.Fatalf("revision did not advance: before %d after %d", before.Revision, after.Revision)
	}
	second, err := s.ThreadStates(ctx, []string{conv}, signals)
	if err != nil {
		t.Fatal(err)
	}
	if got := second[conv]; got.State != threadintel.AnsweredByOwner || got.Reason != threadintel.ReasonOwnerReplied {
		t.Fatalf("rewritten reply state %+v", got)
	}
	if cache.Stats().Parses == parses {
		t.Fatal("a revision bump served the old parsed evidence")
	}
}

// TestThreadStatesContentKeyAcrossStores proves the cache never serves a
// decision from a different mailbox state: two stores whose message rows
// share id and revision but differ in body resolve to their own answer. The
// content digest, not the row id, selects the entry.
func TestThreadStatesContentKeyAcrossStores(t *testing.T) {
	cache := threadintel.SharedCache()
	seed := func(body string, class string) (*Store, string, map[int64]threadintel.Signal) {
		s, _ := open(t, nil)
		conv := "C-same-ids"
		ask := threadTestMessage(1, "store-1", conv, "2026-11-03T08:00:00Z", "client@example.test", "Client")
		ask.HasAttachments, ask.LastVerbExecuted = false, mail.Int(0)
		askID := seedThreadState(t, s, ask, scope.Owner, scope.ReasonRecipientMatch)
		reply := sentThreadMessage(2, "store-1", conv, "2026-11-03T09:00:00Z", "owner@example.test", "Owner")
		reply.Body, reply.MessageClass, reply.HasAttachments, reply.References = body, class, false, ""
		reply.LastVerbExecuted = mail.Int(0)
		reply.InReplyTo = "<packed@example.test>"
		seedThreadState(t, s, reply, scope.Owner, scope.ReasonSentByOwner)
		return s, conv, map[int64]threadintel.Signal{askID: {ReplyNeeded: true}}
	}
	a, conv, signalsA := seed("Done, the plan is attached.", "")
	b, _, signalsB := seed("Thanks", "IPM.Note.Rules.OofTemplate.Microsoft")
	// Same row identity is what makes the test meaningful.
	var revA, revB, idA, idB int64
	if err := a.db.QueryRowContext(t.Context(), `SELECT id, revision FROM messages WHERE conversation_id = ? AND store_id = 'store-1' AND folder_id IN (SELECT id FROM folders WHERE role = 'sent')`, conv).Scan(&idA, &revA); err != nil {
		t.Fatal(err)
	}
	if err := b.db.QueryRowContext(t.Context(), `SELECT id, revision FROM messages WHERE conversation_id = ? AND store_id = 'store-1' AND folder_id IN (SELECT id FROM folders WHERE role = 'sent')`, conv).Scan(&idB, &revB); err != nil {
		t.Fatal(err)
	}
	if idA != idB || revA != revB {
		t.Fatalf("the fixture did not reproduce row identity: a=(%d,%d) b=(%d,%d)", idA, revA, idB, revB)
	}
	cache.Reset()
	statesA, err := a.ThreadStates(t.Context(), []string{conv}, signalsA)
	if err != nil {
		t.Fatal(err)
	}
	statesB, err := b.ThreadStates(t.Context(), []string{conv}, signalsB)
	if err != nil {
		t.Fatal(err)
	}
	if got := statesA[conv]; got.State != threadintel.AnsweredByOwner {
		t.Fatalf("store A state %+v", got)
	}
	if got := statesB[conv]; got.State != threadintel.AwaitingOwner {
		t.Fatalf("store B served store A's parsed evidence: %+v", got)
	}
}

// TestThreadStatesCacheAcrossCloseAndReopen: the parsed-evidence cache is
// deliberately not owned by a Store (it stores no content). A reopen of the
// same database may reuse it, and must produce the identical assessments; a
// different content at the same row identity is proven separate by
// TestThreadStatesContentKeyAcrossStores.
func TestThreadStatesCacheAcrossCloseAndReopen(t *testing.T) {
	s, cfg := open(t, nil)
	ids, signals := seedPackedThreads(t, s)
	cache := threadintel.SharedCache()
	cache.Reset()
	want, err := s.ThreadStates(t.Context(), ids, signals)
	if err != nil {
		t.Fatal(err)
	}
	parses := cache.Stats().Parses
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.ThreadStates(t.Context(), ids, signals)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("a reopened store produced different assessments")
	}
	if cache.Stats().Parses != parses {
		t.Fatalf("the reopened store re-parsed %d messages", cache.Stats().Parses-parses)
	}
}

// TestThreadStatesConcurrentBuilds runs the resolver and the shared cache from
// several goroutines; the race detector is the gate, and every build must
// produce the same assessments.
func TestThreadStatesConcurrentBuilds(t *testing.T) {
	s, _ := open(t, nil)
	ids, signals := seedPackedThreads(t, s)
	want, err := s.ThreadStates(t.Context(), ids, signals)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 3 {
				got, err := s.ThreadStates(context.Background(), ids, signals)
				if err != nil {
					errs <- err
					return
				}
				if !reflect.DeepEqual(got, want) {
					errs <- fmt.Errorf("a concurrent build produced different assessments")
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}
