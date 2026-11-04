package threadintel

import (
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/scope"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func inbound(id int64, when, name, addr string, reply bool, to ...string) Message {
	return Message{RowID: id, ConversationID: "C1", Subject: "Project", SenderName: name, SenderAddress: addr,
		ManyTo: len(to) > 1, Role: mail.RoleInbox, Verdict: scope.Owner, Reason: scope.ReasonRecipientMatch,
		AddressedToMe: mail.AddressedTo, At: at(when), Body: "Could you review the attached plan by Friday?", ReplyNeeded: reply}
}

func ownerSent(id int64, when, body string) Message {
	return Message{RowID: id, ConversationID: "C1", Subject: "Project", SenderName: "Amélie", SenderAddress: "owner@example.test",
		Role: mail.RoleSent, Verdict: scope.Owner, Reason: scope.ReasonSentByOwner, At: at(when), Body: body}
}

func otherSent(id int64, when, name, addr, body string) Message {
	return Message{RowID: id, ConversationID: "C1", Subject: "Project", SenderName: name, SenderAddress: addr,
		Role: mail.RoleSent, Verdict: scope.Shared, Reason: scope.ReasonSentByOther, At: at(when), Body: body}
}

func TestColleagueReplyIsAnsweredByOther(t *testing.T) {
	m1 := inbound(1, "2026-11-03T08:00:00Z", "Client", "client@example.test", true,
		"owner@example.test", "colleague@example.test")
	m2 := otherSent(2, "2026-11-03T09:00:00Z", "Colleague", "colleague@example.test", "I'll take care of it.")
	got := Resolve([]Message{m1, m2}, Options{})
	if got.State != AnsweredByOther || got.NeedsReply() || got.Reason != ReasonOtherReplied {
		t.Fatalf("state %+v", got)
	}
	if got.ActorName != "Colleague" || got.ActorAddress != "colleague@example.test" {
		t.Fatalf("actor %+v", got)
	}
	if got.ReplyToMessageID != 1 {
		t.Fatalf("reply target %d", got.ReplyToMessageID)
	}
}

func TestOwnerReplyIsAnsweredByOwner(t *testing.T) {
	m1 := inbound(1, "2026-11-03T08:00:00Z", "Client", "client@example.test", true, "owner@example.test")
	m2 := ownerSent(2, "2026-11-03T09:00:00Z", "Done, the plan is attached.")
	m2.InReplyTo = "<m1@example.test>"
	got := Resolve([]Message{m1, m2}, Options{})
	if got.State != AnsweredByOwner || got.NeedsReply() || got.ReplyToMessageID != 1 {
		t.Fatalf("state %+v", got)
	}
}

func TestOwnerLastIsAwaitingOther(t *testing.T) {
	m := ownerSent(1, "2026-11-03T09:00:00Z", "Could you send the deck?")
	m.AsksFor = "information_document"
	got := Resolve([]Message{m}, Options{})
	if got.State != AwaitingOther || got.NeedsReply() || got.Reason != ReasonOwnerLast {
		t.Fatalf("state %+v", got)
	}

	// A question mark alone proves nothing: an offer or a closing note
	// carries is_question but asks nothing, so the loop is closed.
	offer := ownerSent(2, "2026-11-03T10:00:00Z", "Let me know if you need anything.")
	offer.IsQuestion = true
	offer.AsksFor = "nothing_fyi"
	if got := Resolve([]Message{offer}, Options{}); got.State != NoAction || got.Reason != ReasonOwnerLastClosed {
		t.Fatalf("offer state %+v", got)
	}
}

func TestOwnerLastNamedAskIsAwaitingOther(t *testing.T) {
	m := ownerSent(1, "2026-11-03T09:00:00Z", "Please send the signed deck by Friday.")
	m.AsksFor = "information_document"
	got := Resolve([]Message{m}, Options{})
	if got.State != AwaitingOther || got.Reason != ReasonOwnerLast {
		t.Fatalf("state %+v", got)
	}
}

func TestOwnerAttachmentWithTrivialCoverNoteClosesTheThread(t *testing.T) {
	ask := inbound(1, "2026-11-03T08:00:00Z", "Client", "client@example.test", true, "owner@example.test")
	reply := ownerSent(2, "2026-11-03T09:00:00Z", "Thanks!")
	reply.InReplyTo = "<m1@example.test>"
	reply.HasAttachments = true
	got := Resolve([]Message{ask, reply}, Options{})
	if got.State != AnsweredByOwner || got.LastMessageID != 2 || got.NeedsReply() {
		t.Fatalf("attachment reply state %+v", got)
	}
}

func TestAutomatedMailNeverOwesAReply(t *testing.T) {
	newsletter := inbound(1, "2026-11-03T08:00:00Z", "News", "news@example.test", true, "owner@example.test")
	newsletter.Automated = true
	newsletter.IsQuestion = true
	got := Resolve([]Message{newsletter}, Options{})
	if got.State != NoAction || got.Reason != ReasonAutomated || got.NeedsReply() {
		t.Fatalf("automated state %+v", got)
	}
}

func TestOutlookOutOfOfficeTemplateIsAutoReply(t *testing.T) {
	ask := inbound(1, "2026-11-03T08:00:00Z", "Client", "client@example.test", true, "owner@example.test")
	ooo := ownerSent(2, "2026-11-03T09:00:00Z", "I am away until Monday.")
	ooo.MessageClass = "IPM.Note.Rules.OofTemplate.Microsoft"
	got := Resolve([]Message{ask, ooo}, Options{})
	if got.State != AwaitingOwner || got.LastMessageID != 1 {
		t.Fatalf("OOO state %+v", got)
	}
}

func TestOwnerClosingNoteIsNotWaiting(t *testing.T) {
	m1 := inbound(1, "2026-11-03T08:00:00Z", "Client", "client@example.test", true, "owner@example.test")
	m2 := ownerSent(2, "2026-11-03T09:00:00Z", "Thanks, all set. Have a good week.")
	m2.InReplyTo = "<m1@example.test>"
	got := Resolve([]Message{m1, m2}, Options{})
	if got.State != AnsweredByOwner || got.Reason != ReasonOwnerReplied || got.NeedsReply() {
		t.Fatalf("state %+v", got)
	}

	// A last owner message with neither a reply header nor an earlier ask
	// is a closing note, not a request.
	closing := ownerSent(3, "2026-11-03T10:00:00Z", "Noted, thanks.")
	got = Resolve([]Message{closing}, Options{})
	if got.State != NoAction || got.Reason != ReasonOwnerLastClosed || got.NeedsReply() {
		t.Fatalf("closing state %+v", got)
	}
}

func TestDirectAnswerRequestWithoutTheReplyFlagStillAwaitsOwner(t *testing.T) {
	// The classifier found a direct answer request but scored reply_needed
	// below its cutoff. The dominant ask still proves the owner owes one.
	direct := inbound(1, "2026-11-03T08:00:00Z", "Client", "client@example.test", false, "owner@example.test")
	direct.IsQuestion = true
	direct.AsksFor = "answer_question"
	if got := Resolve([]Message{direct}, Options{}); got.State != AwaitingOwner {
		t.Fatalf("direct answer request state %+v", got)
	}

	// A question with no answer request is informational: a security alert
	// asking "did you request this?" is not an owner obligation.
	info := direct
	info.AsksFor = "nothing_fyi"
	if got := Resolve([]Message{info}, Options{}); got.State != NoAction || got.Reason != ReasonNoReplyExpected {
		t.Fatalf("informational question state %+v", got)
	}

	// The same answer request CC'd to the owner cannot prove the owner owes
	// the answer.
	cc := direct
	cc.AddressedToMe = mail.AddressedCc
	if got := Resolve([]Message{cc}, Options{}); got.State != NoAction || got.Reason != ReasonCcOnly {
		t.Fatalf("cc question state %+v", got)
	}
	bulk := direct
	bulk.Automated = true
	if got := Resolve([]Message{bulk}, Options{}); got.State != NoAction || got.Reason != ReasonAutomated {
		t.Fatalf("automated question state %+v", got)
	}
}

func TestExternalAnswerClosesTheWait(t *testing.T) {
	ask := ownerSent(1, "2026-11-03T09:00:00Z", "Could you confirm the delivery date?")
	ask.IsQuestion = true
	ask.AsksFor = "answer_question"
	answer := inbound(2, "2026-11-03T10:00:00Z", "Client", "client@example.test", false, "owner@example.test")
	answer.Body = "Yes, the delivery is confirmed for Friday."
	got := Resolve([]Message{ask, answer}, Options{})
	if got.State != AnsweredByOther || got.Reason != ReasonOtherReplied || got.NeedsReply() ||
		got.ReplyToMessageID != 1 {
		t.Fatalf("state %+v", got)
	}
}

func TestExternalMessageAfterOwnerClosingIsNoAction(t *testing.T) {
	closing := ownerSent(1, "2026-11-03T09:00:00Z", "Thanks, all set.")
	notice := inbound(2, "2026-11-03T10:00:00Z", "Client", "client@example.test", false, "owner@example.test")
	notice.Body = "Our office is closed on Monday."
	got := Resolve([]Message{closing, notice}, Options{})
	if got.State != NoAction || got.Reason != ReasonNoReplyExpected || got.NeedsReply() {
		t.Fatalf("state %+v", got)
	}
}

func TestAutoReplyOnlyNamesTheReason(t *testing.T) {
	m := inbound(1, "2026-11-03T08:00:00Z", "Client", "client@example.test", true, "owner@example.test")
	auto := ownerSent(2, "2026-11-03T09:00:00Z", "I am out of office.")
	auto.Headers = "Auto-Submitted: auto-replied\n"
	got := Resolve([]Message{m, auto}, Options{})
	if got.State != AwaitingOwner || got.LastMessageID != 1 {
		t.Fatalf("state %+v", got)
	}
	onlyAuto := Resolve([]Message{auto}, Options{})
	if onlyAuto.State != NoAction || onlyAuto.Reason != ReasonAutoReply {
		t.Fatalf("auto-only state %+v", onlyAuto)
	}
}

func TestInboundNeedsOwner(t *testing.T) {
	m := inbound(1, "2026-11-03T08:00:00Z", "Client", "client@example.test", true, "owner@example.test")
	got := Resolve([]Message{m}, Options{})
	if got.State != AwaitingOwner || !got.NeedsReply() || got.ReplyToMessageID != 1 {
		t.Fatalf("state %+v", got)
	}
}

func TestCcOnlyIsNoAction(t *testing.T) {
	m := inbound(1, "2026-11-03T08:00:00Z", "Client", "client@example.test", false, "team@example.test")
	m.AddressedToMe = mail.AddressedCc
	got := Resolve([]Message{m}, Options{})
	if got.State != NoAction || got.Reason != ReasonCcOnly || got.NeedsReply() {
		t.Fatalf("state %+v", got)
	}
}

func TestTrivialColleagueReplyDoesNotAnswer(t *testing.T) {
	m1 := inbound(1, "2026-11-03T08:00:00Z", "Client", "client@example.test", true,
		"owner@example.test", "colleague@example.test")
	m2 := otherSent(2, "2026-11-03T09:00:00Z", "Colleague", "colleague@example.test", "+1")
	got := Resolve([]Message{m1, m2}, Options{})
	if got.State != AwaitingOwner || !got.NeedsReply() || got.ReplyToMessageID != 1 {
		t.Fatalf("state %+v", got)
	}
}

func TestAutoReplyAndReadReceiptDoNotAnswer(t *testing.T) {
	m1 := inbound(1, "2026-11-03T08:00:00Z", "Client", "client@example.test", true, "owner@example.test")
	auto := ownerSent(2, "2026-11-03T09:00:00Z", "I am out of office.")
	auto.Headers = "Auto-Submitted: auto-replied\n"
	receipt := ownerSent(3, "2026-11-03T09:01:00Z", "")
	receipt.MessageClass = "IPM.Note.ReadReceipt"
	got := Resolve([]Message{m1, auto, receipt}, Options{})
	if got.State != AwaitingOwner || got.LastMessageID != 1 {
		t.Fatalf("state %+v", got)
	}
}

func TestVisibilityGapDoesNotClaimOwnerOwes(t *testing.T) {
	m := inbound(1, "2026-11-03T08:00:00Z", "Client", "client@example.test", true,
		"owner@example.test", "colleague@example.test")
	m.ManyTo = true
	got := Resolve([]Message{m}, Options{Visibility: Partial})
	if got.State != Unknown || got.Reason != ReasonVisibilityGap || got.NeedsReply() {
		t.Fatalf("state %+v", got)
	}

	// One To recipient cannot hide a shared reply: the gap rule reads "more
	// than one To", not "any recipient".
	m.ManyTo = false
	got = Resolve([]Message{m}, Options{Visibility: Partial})
	if got.State != AwaitingOwner || got.Reason != ReasonInboundLast {
		t.Fatalf("single-recipient state %+v", got)
	}
}

func TestOwnerAnswerThenNewInboundReopens(t *testing.T) {
	m1 := inbound(1, "2026-11-03T08:00:00Z", "Client", "client@example.test", true, "owner@example.test")
	m2 := ownerSent(2, "2026-11-03T09:00:00Z", "Done.")
	m2.InReplyTo = "<m1@example.test>"
	m3 := inbound(3, "2026-11-03T10:00:00Z", "Client", "client@example.test", true, "owner@example.test")
	got := Resolve([]Message{m1, m2, m3}, Options{})
	if got.State != AwaitingOwner || got.ReplyToMessageID != 3 {
		t.Fatalf("state %+v", got)
	}
}

func TestNoMessagesIsUnknown(t *testing.T) {
	got := Resolve(nil, Options{})
	if got.State != Unknown || got.Reason != ReasonNoMessages || got.Visibility != UnknownVisibility {
		t.Fatalf("state %+v", got)
	}
}

func TestUnsortedInputIsOrdered(t *testing.T) {
	m1 := inbound(1, "2026-11-03T08:00:00Z", "Client", "client@example.test", true, "owner@example.test")
	m2 := otherSent(2, "2026-11-03T09:00:00Z", "Colleague", "colleague@example.test", "I have it.")
	got := Resolve([]Message{m2, m1}, Options{})
	if got.State != AnsweredByOther || got.LastMessageID != 2 {
		t.Fatalf("state %+v", got)
	}
}

func TestDelegateSendingOnBehalfIsTheOwnerReply(t *testing.T) {
	m1 := inbound(1, "2026-11-03T08:00:00Z", "Client", "client@example.test", true, "owner@example.test")
	byAssistant := Message{RowID: 2, ConversationID: "C1", Subject: "Project", SenderName: "Assistant",
		SenderAddress: "assistant@example.test", SentRepresenting: "owner@example.test", Role: mail.RoleSent,
		Verdict: scope.Owner, Reason: scope.ReasonSentByOwner, At: at("2026-11-03T09:00:00Z"),
		Body: "The owner asked me to send the signed plan."}
	got := Resolve([]Message{m1, byAssistant}, Options{})
	if got.State != AnsweredByOwner || got.NeedsReply() {
		t.Fatalf("state %+v", got)
	}
}

func TestColleagueReplyThenOwnerFollowUpClosesTheLoop(t *testing.T) {
	m1 := inbound(1, "2026-11-03T08:00:00Z", "Client", "client@example.test", true,
		"owner@example.test", "colleague@example.test")
	m2 := otherSent(2, "2026-11-03T09:00:00Z", "Colleague", "colleague@example.test", "I have it.")
	m3 := ownerSent(3, "2026-11-03T10:00:00Z", "Thanks, sending the final version tomorrow.")
	got := Resolve([]Message{m1, m2, m3}, Options{})
	if got.State != AnsweredByOwner || got.NeedsReply() {
		t.Fatalf("state %+v", got)
	}
}

func TestMailingListNotificationDoesNotOweAReply(t *testing.T) {
	m := inbound(1, "2026-11-03T08:00:00Z", "Newsletter", "news@example.test", false, "list@example.test")
	got := Resolve([]Message{m}, Options{})
	if got.State != NoAction || got.NeedsReply() {
		t.Fatalf("state %+v", got)
	}
}

func TestCcReplyFromColleagueIsNoAction(t *testing.T) {
	m := inbound(1, "2026-11-03T08:00:00Z", "Colleague", "colleague@example.test", false, "owner@example.test")
	m.AddressedToMe = mail.AddressedCc
	got := Resolve([]Message{m}, Options{})
	if got.State != NoAction || got.Reason != ReasonCcOnly || got.NeedsReply() {
		t.Fatalf("state %+v", got)
	}
}

func TestStatesListAndParseState(t *testing.T) {
	want := []State{AwaitingOwner, AwaitingOther, AnsweredByOwner, AnsweredByOther, NoAction, Unknown}
	if len(States) != len(want) {
		t.Fatalf("States %v, want %v", States, want)
	}
	for i, st := range want {
		if States[i] != st {
			t.Fatalf("States[%d] = %q, want %q", i, States[i], st)
		}
		got, ok := ParseState(string(st))
		if !ok || got != st {
			t.Fatalf("ParseState(%q) = %q/%v", st, got, ok)
		}
		if got, ok := ParseState("  " + strings.ToUpper(string(st)) + " "); !ok || got != st {
			t.Fatalf("ParseState of a padded upper-case state = %q/%v", got, ok)
		}
	}
	for _, raw := range []string{"", "nope", "awaiting", "unknownx"} {
		if got, ok := ParseState(raw); ok {
			t.Fatalf("ParseState(%q) = %q, true; want false", raw, got)
		}
	}
}

// lastSubstantiveReference is the pre-fast-path rule, kept as the oracle for
// the equivalence test.
func lastSubstantiveReference(msgs []Message, min int) *Message {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := &msgs[i]
		if m.autoReply() {
			continue
		}
		if m.HasAttachments {
			return m
		}
		body := strings.Join(strings.Fields(mail.Fold(m.Body)), " ")
		if body == "" {
			continue
		}
		if trivial(body) {
			continue
		}
		if len([]rune(body)) < min {
			continue
		}
		return m
	}
	return nil
}

func TestSubstantiveFastPathMatchesFold(t *testing.T) {
	long := strings.Repeat("This is a substantive sentence about the plan. ", 80)
	nfkThanks := strings.Repeat("ｔｈａｎｋｓ", 20)
	emoji := strings.Repeat("👍", 200)
	bodies := []string{
		"", "   ", "ok", "Thanks", "thank you", "bien recu", "+1", "👍",
		long, strings.Repeat(" ", 5000) + long, strings.Repeat("é", 200),
		strings.Repeat("e\u0301", 200), nfkThanks, emoji, strings.Repeat("日本語", 100),
		strings.Repeat("a", 63) + " thanks", strings.Repeat("a", 65),
	}
	for _, body := range bodies {
		msgs := []Message{{
			RowID: 1, ConversationID: "C1", Role: mail.RoleInbox, Verdict: scope.Owner,
			Reason: scope.ReasonRecipientMatch, At: at("2026-11-03T08:00:00Z"), Body: body,
		}}
		thread := NewThread(msgs, nil)
		fast, slow := lastSubstantiveOf(thread), lastSubstantiveReference(msgs, 1)
		if (fast == nil) != (slow == nil) || (fast != nil && fast.RowID != slow.RowID) {
			t.Fatalf("body %q: fast %v, reference %v", body, fast, slow)
		}
	}
	// A message carrying a file is substantive whatever its cover note:
	// the attachment is the move, so a bare "thanks" cannot be skipped and
	// leave an older inbound looking unanswered. Both paths must agree.
	for _, body := range []string{"", "ok"} {
		withAttachment := Message{RowID: 1, ConversationID: "C1", Role: mail.RoleInbox, Verdict: scope.Owner,
			Reason: scope.ReasonRecipientMatch, At: at("2026-11-03T08:00:00Z"), Body: body, HasAttachments: true}
		thread := NewThread([]Message{withAttachment}, nil)
		fast, slow := lastSubstantiveOf(thread), lastSubstantiveReference([]Message{withAttachment}, 1)
		if (fast == nil) != (slow == nil) || (fast != nil && fast.RowID != slow.RowID) {
			t.Fatalf("attachment body %q: fast %v, reference %v", body, fast, slow)
		}
	}
}

// lastSubstantiveOf adapts the Thread memo to the oracle comparison above.
func lastSubstantiveOf(t *Thread) *Message {
	if i := t.lastSubstantive(1); i >= 0 {
		return &t.messages[i]
	}
	return nil
}

// EvidenceFrontier walks newest-first and asks for exactly the next message
// the resolver cannot classify yet. A loaded substantive message stops the
// search; auto-replies and trivial acknowledgements only advance it.
func TestEvidenceFrontierLoadsUntilSubstantive(t *testing.T) {
	thread := []Message{
		inbound(1, "2026-11-03T08:00:00Z", "Client", "client@example.test", true),
		inbound(2, "2026-11-03T09:00:00Z", "Owner", "owner@example.test", false),
		inbound(3, "2026-11-03T10:00:00Z", "Auto", "auto@example.test", false),
	}
	thread[1].Body = "Thanks"
	thread[2].Body = "I am away until Monday."
	thread[2].MessageClass = "IPM.Note.Rules.OofTemplate.Microsoft"
	loaded := map[int64]struct{}{}
	want := []int64{3, 2, 1}
	for _, id := range want {
		got, ok := EvidenceFrontier(thread, loaded, Options{})
		if !ok || got != id {
			t.Fatalf("frontier = %d %v, want %d", got, ok, id)
		}
		thread[id-1].Headers = "X-Test: loaded"
		loaded[id] = struct{}{}
	}
	if id, ok := EvidenceFrontier(thread, loaded, Options{}); ok {
		t.Fatalf("a loaded substantive message left frontier %d", id)
	}
	// A loaded substantive message ends the walk even when older mail is
	// still unloaded: the resolver would never read it.
	thread = append(thread, inbound(0, "2026-11-03T07:00:00Z", "Old", "old@example.test", true))
	loaded = map[int64]struct{}{1: {}, 2: {}, 3: {}}
	if id, ok := EvidenceFrontier(thread, loaded, Options{}); ok {
		t.Fatalf("frontier went past the newest substantive message to %d", id)
	}
}

func TestEvidenceFrontierAttachmentAndEmptyBody(t *testing.T) {
	thread := []Message{
		inbound(1, "2026-11-03T08:00:00Z", "Client", "client@example.test", true),
		inbound(2, "2026-11-03T09:00:00Z", "Owner", "owner@example.test", false),
	}
	thread[1].Body = ""
	thread[1].HasAttachments = true
	loaded := map[int64]struct{}{1: {}}
	if id, ok := EvidenceFrontier(thread, loaded, Options{}); !ok || id != 2 {
		t.Fatalf("frontier = %d %v, want the attachment row 2", id, ok)
	}
	loaded[2] = struct{}{}
	if id, ok := EvidenceFrontier(thread, loaded, Options{}); ok {
		t.Fatalf("the attachment did not stop the frontier: %d", id)
	}
}

// A capitalized acknowledgement is still an acknowledgement; treating
// "Thanks" as substance closed a thread that still owed the client a reply.
func TestTrivialAcknowledgementFoldsCase(t *testing.T) {
	thread := []Message{
		inbound(1, "2026-11-03T08:00:00Z", "Client", "client@example.test", true),
		ownerSent(2, "2026-11-03T09:00:00Z", "Thanks"),
	}
	got := Resolve(thread, Options{})
	if got.State != AwaitingOwner || got.Reason != ReasonInboundLast {
		t.Fatalf("a capitalized thank-you closed the thread: %+v", got)
	}
}

// The open set and the display order are one policy: owner obligations,
// the other side, then undecidable; closed states are not open.
func TestOpenStatePolicyAndOrder(t *testing.T) {
	for _, state := range []State{AwaitingOwner, AwaitingOther, Unknown} {
		if !IsOpen(state) {
			t.Errorf("%s is not open", state)
		}
	}
	for _, state := range []State{AnsweredByOwner, AnsweredByOther, NoAction} {
		if IsOpen(state) {
			t.Errorf("%s is open", state)
		}
	}
	if got := StateOrder(AwaitingOwner); got != 0 {
		t.Fatalf("owner order = %d, want 0", got)
	}
	if got := StateOrder(Unknown); got != len(States)-1 {
		t.Fatalf("unknown order = %d, want %d", got, len(States)-1)
	}
	if got := StateOrder(State("bogus")); got != len(States) {
		t.Fatalf("an undeclared state sorted at %d, want after the list", got)
	}
}

// OpenTotal sums exactly the open policy, Unknown included: a profile whose
// visible evidence cannot decide a thread still owes an open count.
func TestOpenTotalSumsOpenStatesWithUnknown(t *testing.T) {
	counts := map[string]int{
		string(AwaitingOwner):   2,
		string(AwaitingOther):   3,
		string(Unknown):         4,
		string(AnsweredByOwner): 11,
		string(AnsweredByOther): 7,
		string(NoAction):        5,
	}
	if got := OpenTotal(counts); got != 9 {
		t.Fatalf("OpenTotal = %d, want 9 (owner 2 + other 3 + unknown 4)", got)
	}
	if got := OpenTotal(nil); got != 0 {
		t.Fatalf("OpenTotal(nil) = %d, want 0", got)
	}
}

// TestClassifyAskIsTheOneVocabulary pins the structural meaning of every
// profile ask. The resolver's "asks the other side" rule and the owner-facing
// classifier both read this classification, so an option cannot be concrete
// work in one and unrecognized in the other.
func TestClassifyAskIsTheOneVocabulary(t *testing.T) {
	cases := map[string]struct {
		kind  Ask
		other bool
	}{
		"answer_question":      {AskReply, true},
		"approval_decision":    {AskWork, true},
		"information_document": {AskWork, true},
		"payment_money":        {AskWork, true},
		"meeting_availability": {AskWork, true},
		"task_work":            {AskWork, true},
		"nothing_fyi":          {AskFYI, false},
		"other":                {AskNone, false},
		"":                     {AskNone, false},
		"  PAYMENT_MONEY  ":    {AskWork, true},
	}
	for asksFor, want := range cases {
		kind := ClassifyAsk(asksFor)
		if kind != want.kind {
			t.Errorf("ClassifyAsk(%q) = %v, want %v", asksFor, kind, want.kind)
		}
		if got := kind.AsksOther(); got != want.other {
			t.Errorf("ClassifyAsk(%q).AsksOther() = %v, want %v", asksFor, got, want.other)
		}
	}
	// The message method is the same rule: one owner, no second switch.
	for asksFor, want := range cases {
		m := &Message{AsksFor: asksFor}
		if got := m.asksOther(); got != want.other {
			t.Errorf("message %q asksOther %v, want %v", asksFor, got, want.other)
		}
	}
}
