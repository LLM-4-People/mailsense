// Package threadintel resolves the state of a conversation from visible
// messages, including shared-mailbox replies that the owner-only corpus
// deliberately excludes. It answers one question: does the owner still owe
// a reply, did the owner answer, did someone else answer, or can the
// visible evidence not tell? It is pure: callers supply the messages.
//
// The resolver is structural, not a phrase table. It uses message direction,
// scope verdicts, sender/sent-representing identity, recipient multiplicity,
// reply headers, auto-reply headers and the message-level reply signal the
// classifier already produced. Language understanding stays in the
// classifier/planner; this layer owns thread truth.
package threadintel

import (
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/scope"
)

// State is the resolved state of one conversation.
type State string

// Thread states. Only AwaitingOwner means the owner still owes a reply.
const (
	AwaitingOwner   State = "awaiting_owner"
	AwaitingOther   State = "awaiting_other"
	AnsweredByOwner State = "answered_by_owner"
	AnsweredByOther State = "answered_by_other"
	NoAction        State = "no_action"
	Unknown         State = "unknown"
)

// States is the one ordered list of conversation states. Counts, filters and
// validation read it so a new state cannot be handled on one screen and
// forgotten on another.
var States = []State{AwaitingOwner, AwaitingOther, AnsweredByOwner, AnsweredByOther, NoAction, Unknown}

// OpenStates is the one open-thread policy: a conversation still asks the
// owner or the other side for something, or the visible evidence cannot
// decide. Every surface that counts or lists open threads reads this list,
// so a state cannot be open on one screen and closed on another.
var OpenStates = []State{AwaitingOwner, AwaitingOther, Unknown}

// IsOpen reports whether state is one of OpenStates.
func IsOpen(state State) bool {
	return slices.Contains(OpenStates, state)
}

// StateCounts returns one count per state in States, reading a state missing
// from sparse as zero. Every surface that shows the vocabulary (a company
// profile, the conversations page, the conversations facts, the wire view)
// uses it, so the whole vocabulary is always present and a state can never be
// missing from one screen and zero on another. An unknown key in sparse is
// never invented onto the wire.
func StateCounts(sparse map[string]int) map[string]int {
	counts := make(map[string]int, len(States))
	for _, state := range States {
		counts[string(state)] = sparse[string(state)]
	}
	return counts
}

// OpenTotal is the number of thread states that count as open. It is the one
// sum over OpenStates; surfaces with a deliberately different definition
// (owner-only obligations, message-level reply counts) do not use it.
func OpenTotal(counts map[string]int) int {
	total := 0
	for _, state := range OpenStates {
		total += counts[string(state)]
	}
	return total
}

// StateOrder is the display priority of the open list: owner obligations
// first, then waiting on the other side, then undecidable. An unknown value
// sorts after every declared state instead of jumping to the front.
func StateOrder(state State) int {
	if i := slices.Index(States, state); i >= 0 {
		return i
	}
	return len(States)
}

// ParseState reads a query state. An empty or unknown value is not a state;
// callers treat an empty query value as no state filter.
func ParseState(s string) (State, bool) {
	st := State(strings.ToLower(strings.TrimSpace(s)))
	if st == "" || !slices.Contains(States, st) {
		return "", false
	}
	return st, true
}

// Reason names the evidence behind a state.
type Reason string

// State reasons.
const (
	ReasonOwnerReplied    Reason = "owner_replied"
	ReasonOtherReplied    Reason = "other_replied"
	ReasonOwnerLast       Reason = "owner_last"
	ReasonOwnerLastClosed Reason = "owner_last_closed"
	ReasonInboundLast     Reason = "inbound_last"
	ReasonCcOnly          Reason = "cc_only"
	ReasonNoReplyExpected Reason = "no_reply_expected"
	ReasonVisibilityGap   Reason = "visibility_gap"
	ReasonNoMessages      Reason = "no_messages"
	ReasonAutoReply       Reason = "auto_reply"
	ReasonAutomated       Reason = "automated_mail"
)

// Visibility says how complete the thread evidence is.
type Visibility string

// Evidence completeness.
const (
	Complete          Visibility = "complete"
	Partial           Visibility = "partial"
	UnknownVisibility Visibility = "unknown"
)

// Signal is the classifier evidence the resolver needs for one owner row.
// A store maps it onto Message; shared rows have no signal. Topic and
// Subtopic are carried only so a conversation reader can name the subject of
// the last move without reading the body again.
type Signal struct {
	ReplyNeeded bool
	IsQuestion  bool
	AsksFor     string
	Topic       string
	Subtopic    string
	// Automated marks machine-generated bulk mail. It can contain a question
	// or a call to action, but it never owes the owner a reply.
	Automated bool
}

// Message is one thread member as the resolver reads it. Body and headers
// are used only to reject auto-replies and trivial acknowledgements; they
// are never returned by an assessment.
type Message struct {
	RowID int64
	// Revision is messages.revision: the store's change marker for this row.
	// It is part of the parsed-evidence cache key, never part of a decision.
	Revision         int64
	ConversationID   string
	Subject          string
	SenderName       string
	SenderAddress    string
	SentRepresenting string
	Role             mail.FolderRole
	Verdict          scope.Verdict
	Reason           scope.Reason
	AddressedToMe    string
	// ManyTo reports whether the message has more than one To recipient.
	// The resolver reads it only to decide whether partial visible evidence
	// could have hidden a shared reply to a multi-recipient ask.
	ManyTo            bool
	At                time.Time
	Body              string
	Headers           string
	MessageClass      string
	InternetMessageID string
	InReplyTo         string
	References        string
	LastVerbExecuted  int
	// ReplyNeeded is the classifier's directed answer request: a query or
	// request aimed at the mailbox owner. IsQuestion is the wider text
	// signal; AsksFor is the chosen ask. The owner-last rule needs all three
	// to tell a real request from a closing thank-you.
	ReplyNeeded    bool
	IsQuestion     bool
	AsksFor        string
	Topic          string
	Subtopic       string
	Automated      bool
	HasAttachments bool
	// OwnerFacing is set by the store when this message may contribute
	// owner-facing content. Shared/delegate/public mail still affects the
	// state but never the displayed actor.
	OwnerFacing bool
	// OtherOwner marks a message whose scope verdict says "owner" for a
	// shared/delegate/public mailbox, not for the owner of this mailbox. A
	// colleague's reply must read as other, not as the owner's own reply.
	OtherOwner bool
}

// Options controls the evidence boundary.
type Options struct {
	// Visibility is Partial when a shared/delegate store that could hold a
	// reply is not picked, or when the caller otherwise knows the thread is
	// incomplete. The resolver never treats missing evidence as "no reply".
	Visibility   Visibility
	MinBodyRunes int
}

// Assessment is the resolved state of one conversation.
type Assessment struct {
	ConversationID string `json:"conversation_id,omitempty"`
	State          State  `json:"state"`
	Reason         Reason `json:"reason"`
	ActorName      string `json:"actor_name,omitempty"`
	ActorAddress   string `json:"actor_address,omitempty"`
	LastAt         string `json:"last_at,omitempty"`
	LastMessageID  int64  `json:"last_message_id,omitempty"`
	// OwnerActorName and OwnerActorAddress name the actor only when the
	// state actor is also owner-facing content. They stay empty when the
	// last move came from shared/delegate mail, so a content reader can
	// omit the name instead of leaking a participant.
	OwnerActorName     string     `json:"owner_actor_name,omitempty"`
	OwnerActorAddress  string     `json:"owner_actor_address,omitempty"`
	OwnerLastAt        string     `json:"owner_last_at,omitempty"`
	OwnerLastMessageID int64      `json:"owner_last_message_id,omitempty"`
	ReplyToMessageID   int64      `json:"reply_to_message_id,omitempty"`
	Visibility         Visibility `json:"visibility"`
	Confidence         float64    `json:"confidence"`
	Messages           int        `json:"messages"`
	// Topic and Subtopic are the last substantive move's subject evidence.
	// They are identifiers, never display copy.
	Topic    string `json:"topic,omitempty"`
	Subtopic string `json:"subtopic,omitempty"`
}

// NeedsReply reports whether the owner still owes a reply.
func (a Assessment) NeedsReply() bool { return a.State == AwaitingOwner }

// Resolve computes the state of one conversation. The input is not mutated.
// It is the stateless form of Thread: one call, no cross-pass memo, no cache.
func Resolve(thread []Message, opts Options) Assessment {
	return NewThread(thread, nil).Resolve(opts)
}

// Thread is one conversation under resolution. It owns the ordered member
// list and the parsed-evidence decision of each member, so the frontier walk
// and the final resolve parse a message at most once per content state and
// resolution option. A caller that loads deferred bodies after the first
// frontier call reports them through SetContent, which invalidates only that
// member.
//
// A Thread is one build's scratch state: it is not safe for concurrent use.
// The optional Cache is shared and concurrency-safe.
type Thread struct {
	messages []Message
	// order is the resolver's (instant, row id) order as indexes into
	// messages, built once. The stateless path re-sorts a copy on every
	// frontier round; the order cannot change because SetContent replaces
	// only body and headers.
	order    []int
	evidence []memberEvidence
	cache    *Cache
}

// memberEvidence is the per-member memo: the decided evidence and the body
// minimum it was decided under. A different minimum (a caller can resolve
// the same Thread with another Options) invalidates only that decision.
type memberEvidence struct {
	value parsedEvidence
	min   int
	valid bool
}

// NewThread returns the resolution state of one conversation. It copies the
// input, so the caller keeps ownership of its slice. cache may be nil for a
// pure one-pass resolution; SharedCache adds cross-build reuse.
func NewThread(messages []Message, cache *Cache) *Thread {
	t := &Thread{
		messages: append([]Message(nil), messages...),
		order:    make([]int, len(messages)),
		evidence: make([]memberEvidence, len(messages)),
		cache:    cache,
	}
	for i := range t.order {
		t.order[i] = i
	}
	sort.SliceStable(t.order, func(i, j int) bool {
		a, b := &t.messages[t.order[i]], &t.messages[t.order[j]]
		if !a.At.Equal(b.At) {
			return a.At.Before(b.At)
		}
		return a.RowID < b.RowID
	})
	return t
}

// SetContent installs the deferred body and headers the caller loaded for one
// member, by its position in the slice NewThread received. The member's
// parsed evidence is recomputed on next use; order and row identity do not
// change, so the frontier walk keeps its position.
func (t *Thread) SetContent(index int, body, headers string) {
	t.messages[index].Body = body
	t.messages[index].Headers = headers
	t.evidence[index].valid = false
}

// Resolve computes the state of the thread. The input messages are not
// mutated.
func (t *Thread) Resolve(opts Options) Assessment {
	opts = resolvedOptions(opts)
	out := t.resolveState(opts)
	if name, address, at, id, ok := lastOwnerFacing(t.messages); ok &&
		sameActor(out.ActorName, out.ActorAddress, name, address) {
		out.OwnerActorName, out.OwnerActorAddress = name, address
		out.OwnerLastAt, out.OwnerLastMessageID = at, id
	}
	return out
}

// resolvedOptions applies the one option normalization of the resolver:
// absent visibility is complete, and a non-positive body minimum is one rune.
// Both the frontier walk and the final resolve use it, so the per-member memo
// speaks about the same decision in both phases.
func resolvedOptions(opts Options) Options {
	if opts.Visibility == "" {
		opts.Visibility = Complete
	}
	if opts.MinBodyRunes <= 0 {
		opts.MinBodyRunes = 1
	}
	return opts
}

// lastOwnerFacing returns the newest message that may contribute
// owner-facing content: the actor a content reader is allowed to name.
func lastOwnerFacing(thread []Message) (name, address, at string, id int64, ok bool) {
	var best *Message
	for i := range thread {
		m := &thread[i]
		if !m.OwnerFacing {
			continue
		}
		if best == nil || m.At.After(best.At) || (m.At.Equal(best.At) && m.RowID > best.RowID) {
			best = m
		}
	}
	if best == nil {
		return "", "", "", 0, false
	}
	return best.SenderName, best.SenderAddress, instant(best.At), best.RowID, true
}

// sameActor compares two message actors by address when both carry one,
// otherwise by folded name. It never treats two empty addresses as a match
// just because both names are empty.
func sameActor(aName, aAddress, bName, bAddress string) bool {
	aAddress, bAddress = strings.TrimSpace(aAddress), strings.TrimSpace(bAddress)
	if aAddress != "" || bAddress != "" {
		return aAddress != "" && bAddress != "" && strings.EqualFold(aAddress, bAddress)
	}
	aName, bName = strings.TrimSpace(aName), strings.TrimSpace(bName)
	return aName != "" && strings.EqualFold(aName, bName)
}

// resolveState is the state decision, without the owner-facing actor
// projection. Early returns keep each state rule readable.
func (t *Thread) resolveState(opts Options) Assessment {
	msgs := t.messages
	out := Assessment{Visibility: opts.Visibility, Messages: len(msgs)}
	if len(msgs) == 0 {
		out.State, out.Reason, out.Confidence = Unknown, ReasonNoMessages, 0
		out.Visibility = UnknownVisibility
		return out
	}
	for _, i := range t.order {
		if out.ConversationID == "" && msgs[i].ConversationID != "" {
			out.ConversationID = msgs[i].ConversationID
		}
	}
	lastIndex := t.lastSubstantive(opts.MinBodyRunes)
	if lastIndex < 0 {
		out.State, out.Reason = NoAction, ReasonNoReplyExpected
		if t.anyAutoReply(opts.MinBodyRunes) {
			out.Reason = ReasonAutoReply
		}
		out.LastAt = instant(msgs[t.order[len(t.order)-1]].At)
		out.Confidence = 0.6
		return out
	}
	last := &msgs[lastIndex]
	out.LastMessageID, out.LastAt = last.RowID, instant(last.At)
	out.Topic, out.Subtopic = last.Topic, last.Subtopic
	if out.Topic == "" {
		// A shared-mailbox colleague's substantive reply carries no
		// classifier signal. The subject of the conversation is still the
		// newest message that has one, never blank on answered threads.
		for i := len(t.order) - 1; i >= 0; i-- {
			if m := &msgs[t.order[i]]; m.Topic != "" {
				out.Topic, out.Subtopic = m.Topic, m.Subtopic
				break
			}
		}
	}

	switch {
	case last.sentByOwner():
		prev := lastInboundBefore(msgs, last)
		if last.asksOther() {
			out.State, out.Reason = AwaitingOther, ReasonOwnerLast
			out.ActorName, out.ActorAddress = last.SenderName, last.SenderAddress
			out.Confidence = 0.85
			return out
		}
		if last.isReply(prev) {
			out.State, out.Reason = AnsweredByOwner, ReasonOwnerReplied
			out.ActorName, out.ActorAddress = last.SenderName, last.SenderAddress
			if prev != nil {
				out.ReplyToMessageID = prev.RowID
			}
			out.Confidence = 0.9
			return out
		}
		// The owner moved last with a closing note, a thank-you or an FYI:
		// the loop is closed, not waiting on anyone.
		out.State, out.Reason = NoAction, ReasonOwnerLastClosed
		out.ActorName, out.ActorAddress = last.SenderName, last.SenderAddress
		out.Confidence = 0.8
		return out

	case last.sentByOther():
		prev := lastInboundBefore(msgs, last)
		out.State, out.Reason = AnsweredByOther, ReasonOtherReplied
		out.ActorName, out.ActorAddress = last.SenderName, last.SenderAddress
		if prev != nil {
			out.ReplyToMessageID = prev.RowID
		}
		out.Confidence = 0.95
		return out

	default: // inbound
		out.ActorName, out.ActorAddress = last.SenderName, last.SenderAddress
		// Bulk mail never owes a reply, however its text is phrased. This
		// runs before the reply answer so a newsletter question cannot
		// become an owner obligation.
		if last.Automated {
			out.State, out.Reason = NoAction, ReasonAutomated
			out.Confidence = 0.85
			return out
		}
		if last.AddressedToMe == mail.AddressedCc && !last.ReplyNeeded {
			out.State, out.Reason = NoAction, ReasonCcOnly
			out.Confidence = 0.75
			return out
		}
		if last.asksOwner() {
			// A visible shared reply would have been the last substantive
			// message. Without one, a multi-recipient thread whose shared
			// evidence is incomplete stays unknown rather than claiming the
			// owner owes a reply.
			if opts.Visibility == Partial && last.ManyTo {
				out.State, out.Reason = Unknown, ReasonVisibilityGap
				out.Confidence = 0.4
				return out
			}
			out.State, out.Reason = AwaitingOwner, ReasonInboundLast
			out.ReplyToMessageID = last.RowID
			out.Confidence = 0.85
			return out
		}
		// The other side replied to the owner's own question. That closes
		// the wait; it is not an owner obligation. A new inbound after a
		// closing note stays the ordinary no-action case below.
		if prev := lastOutboundBefore(msgs, last); prev != nil && prev.asksOther() {
			out.State, out.Reason = AnsweredByOther, ReasonOtherReplied
			out.ReplyToMessageID = prev.RowID
			out.Confidence = 0.9
			return out
		}
		out.State, out.Reason = NoAction, ReasonNoReplyExpected
		out.Confidence = 0.7
		return out
	}
}

// lastSubstantive returns the index of the newest real thread move, or -1.
// It reads the parsed-evidence memo, so a message examined by the frontier
// walk is not parsed again here.
func (t *Thread) lastSubstantive(min int) int {
	for i := len(t.order) - 1; i >= 0; i-- {
		index := t.order[i]
		if t.evidenceAt(index, min).substantive {
			return index
		}
	}
	return -1
}

// anyAutoReply reports whether the thread contains a skipped auto-reply or
// read receipt, so the no-substantive-move reason can name it.
func (t *Thread) anyAutoReply(min int) bool {
	for _, index := range t.order {
		if t.evidenceAt(index, min).autoReply {
			return true
		}
	}
	return false
}

// evidenceAt returns one member's parsed evidence, reusing the per-thread
// memo and then the shared cache. min is already normalized to >= 1.
func (t *Thread) evidenceAt(index, min int) parsedEvidence {
	if t.evidence[index].valid && t.evidence[index].min == min {
		return t.evidence[index].value
	}
	m := &t.messages[index]
	var ev parsedEvidence
	if t.cache != nil {
		key := evidenceKeyOf(m, min)
		if cached, ok := t.cache.get(key); ok {
			ev = cached
		} else {
			t.cache.recordParse()
			ev = m.parse(min)
			t.cache.put(key, ev)
		}
	} else {
		ev = m.parse(min)
	}
	t.evidence[index] = memberEvidence{value: ev, min: min, valid: true}
	return ev
}

// substantive is the one rule for a real thread move. It owns the fast path
// (a body with enough ASCII cannot fold to a trivial acknowledgement), the
// attachment exception and the folded-body minimum. lastSubstantive and
// EvidenceFrontier must share it: a query that decides "enough evidence" with
// another rule can stop one message too early or fetch one too many.
func (m *Message) substantive(min int) bool { return m.parse(min).substantive }

// parse is the one computation of the parsed-evidence decision. It is the
// only path that reads Headers and Body, so caching its result exactly
// preserves every downstream rule.
func (m *Message) parse(min int) parsedEvidence {
	ev := parsedEvidence{autoReply: m.autoReply()}
	if ev.autoReply {
		return ev
	}
	// A body carrying enough ASCII letters or digits cannot fold to empty,
	// cannot fold to a known acknowledgement token, and is longer than any
	// minimum body length. Returning it directly skips folding and splitting
	// a multi-kilobyte body that the resolver would keep anyway.
	if min <= substantiveFastRunes && substantiveEnough(m.Body) {
		ev.substantive = true
		return ev
	}
	// A message carrying a file is a real move even when its cover note is
	// a bare "thanks": the attachment is the substance.
	if m.HasAttachments {
		ev.substantive = true
		return ev
	}
	body := strings.Join(strings.Fields(mail.Fold(m.Body)), " ")
	if body == "" || trivial(body) {
		return ev
	}
	ev.substantive = len([]rune(body)) >= min
	return ev
}

// EvidenceFrontier returns the newest member whose Body and Headers are not
// loaded yet and whose classification can still decide lastSubstantive.
// Loaded auto-replies and trivial acknowledgements are skipped; a loaded
// substantive member ends the search. The caller fetches the returned row,
// applies it with SetContent, and calls again until ok is false.
func (t *Thread) EvidenceFrontier(loaded map[int64]struct{}, opts Options) (int64, bool) {
	min := opts.MinBodyRunes
	if min <= 0 {
		min = 1
	}
	for i := len(t.order) - 1; i >= 0; i-- {
		index := t.order[i]
		m := &t.messages[index]
		if _, ok := loaded[m.RowID]; !ok {
			return m.RowID, true
		}
		if t.evidenceAt(index, min).substantive {
			return 0, false
		}
	}
	return 0, false
}

// EvidenceFrontier is the stateless form of Thread.EvidenceFrontier: one
// walk with no cross-call memo. Callers that repeat the walk over one
// conversation should keep a Thread and load through SetContent.
func EvidenceFrontier(thread []Message, loaded map[int64]struct{}, opts Options) (int64, bool) {
	return NewThread(thread, nil).EvidenceFrontier(loaded, opts)
}

// substantiveFastRunes is the number of ASCII letters or digits that makes a
// body certainly substantive. It is larger than the longest trivial reply
// ("thank you", "bien recu") with a wide margin.
const substantiveFastRunes = 64

// substantiveEnough reports whether body carries substantiveFastRunes ASCII
// letters or digits. ASCII is preserved by mail.Fold, so such a body cannot
// become empty or collapse to a short acknowledgement.
func substantiveEnough(body string) bool {
	n := 0
	for i := 0; i < len(body) && n < substantiveFastRunes; i++ {
		c := body[i]
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
			n++
		}
	}
	return n >= substantiveFastRunes
}

// closestBefore is the one "nearest earlier message" rule: the newest message
// strictly before m that accept selects, with the row id breaking a timestamp
// tie. The inbound and outbound finders differ only in their direction.
func closestBefore(msgs []Message, m *Message, accept func(*Message) bool) *Message {
	var best *Message
	for i := range msgs {
		c := &msgs[i]
		if c.RowID == m.RowID || !accept(c) {
			continue
		}
		if c.At.After(m.At) || (c.At.Equal(m.At) && c.RowID > m.RowID) {
			continue
		}
		if best == nil || c.At.After(best.At) || (c.At.Equal(best.At) && c.RowID > best.RowID) {
			best = c
		}
	}
	return best
}

// lastInboundBefore returns the closest received message before m.
func lastInboundBefore(msgs []Message, m *Message) *Message {
	return closestBefore(msgs, m, (*Message).inbound)
}

// lastOutboundBefore returns the closest message the owner's side sent before
// m. Shared colleagues count: their message moved the conversation too.
func lastOutboundBefore(msgs []Message, m *Message) *Message {
	return closestBefore(msgs, m, func(c *Message) bool { return !c.inbound() })
}

func (m *Message) sentByOwner() bool {
	return m.Verdict == scope.Owner && m.Reason == scope.ReasonSentByOwner && !m.OtherOwner
}

func (m *Message) sentByOther() bool {
	if m.sentByOwner() {
		return false
	}
	return m.Reason == scope.ReasonSentByOther || m.Role == mail.RoleSent
}

func (m *Message) inbound() bool { return !m.sentByOwner() && !m.sentByOther() }

// asksOwner reports whether an inbound message asks the mailbox owner for an
// answer. It reads the directed answers only: reply_needed, or the dominant
// ask being an answer. is_question is deliberately undirected ("regardless of
// who must answer") and is never used here: a security alert asking "did you
// request this?" is informational, not an owner obligation.
func (m *Message) asksOwner() bool {
	if m.Automated {
		return false
	}
	return m.ReplyNeeded || ClassifyAsk(m.AsksFor) == AskReply
}

// asksOther reports whether an outbound message asks the other side for
// something. The dominant ask is the directed evidence; a closing note, a
// delivery, a thank-you or an offer ("let me know if you need anything")
// carries no concrete ask and therefore closes the loop instead of waiting.
func (m *Message) asksOther() bool {
	return ClassifyAsk(m.AsksFor).AsksOther()
}

// Ask is the structural meaning of the profile's dominant ask. It is the one
// classification of the asks_for vocabulary: the resolver's "does this ask the
// other side for something" and the owner-facing classifier's action table
// both read it, so a new profile option cannot be concrete in one and
// unrecognized in the other.
type Ask int

const (
	// AskNone is no recognized ask: an empty value or the profile's "other".
	AskNone Ask = iota
	// AskReply asks for an answer or a discussion.
	AskReply
	// AskWork asks for concrete work: a decision, a document, money, time or
	// a task.
	AskWork
	// AskFYI asks for nothing.
	AskFYI
)

// The asks_for vocabulary the default profile declares. ClassifyAsk is the one
// classification; a caller that needs a specific option (the action a work ask
// implies) reads its constant instead of repeating the string.
const (
	AskAnswerQuestion      = "answer_question"
	AskApprovalDecision    = "approval_decision"
	AskInformationDocument = "information_document"
	AskPaymentMoney        = "payment_money"
	AskMeetingAvailability = "meeting_availability"
	AskTaskWork            = "task_work"
	AskNothingFYI          = "nothing_fyi"
)

// AskOption is the canonical spelling of a dominant ask: trimmed and
// lower-cased, "" when absent. ClassifyAsk and a caller that needs the specific
// option both read it, so the two cannot normalize differently.
func AskOption(asksFor string) string { return strings.ToLower(strings.TrimSpace(asksFor)) }

// ClassifyAsk maps the profile's dominant ask to its structural meaning. The
// vocabulary is the default profile's asks_for criteria; an option it does not
// name is AskNone, never a guess.
func ClassifyAsk(asksFor string) Ask {
	switch AskOption(asksFor) {
	case AskAnswerQuestion:
		return AskReply
	case AskApprovalDecision, AskInformationDocument, AskPaymentMoney, AskMeetingAvailability, AskTaskWork:
		return AskWork
	case AskNothingFYI:
		return AskFYI
	default:
		return AskNone
	}
}

// AsksOther reports whether the ask is directed at the other side: a question
// to answer or work to do. An FYI, an unrecognized option and an empty value
// ask nothing.
func (a Ask) AsksOther() bool { return a == AskReply || a == AskWork }

// isReply reports whether m answers the conversation rather than starting
// a new request. Reply headers and the last-verb marker are direct
// evidence; a message sent after an inbound that asked for a reply is the
// structural case (a delegate or a plain new message has no headers).
func (m *Message) isReply(prev *Message) bool {
	if strings.TrimSpace(m.InReplyTo) != "" || strings.TrimSpace(m.References) != "" {
		return true
	}
	if prev == nil {
		return false
	}
	if prev.LastVerbExecuted == 102 || prev.LastVerbExecuted == 103 {
		return true
	}
	return prev.ReplyNeeded
}

func (m *Message) autoReply() bool {
	// mail.KindOf is the one protocol-data classification: it knows the
	// Outlook item classes (including OOF templates), RFC 3834 headers and
	// report types. A resolver that re-parsed message classes here missed
	// the most common Outlook out-of-office item.
	switch mail.KindOf(&mail.Message{MessageClass: m.MessageClass, Headers: m.Headers}) {
	case mail.KindAutoReply, mail.KindReadReceipt, mail.KindDeliveryReport, mail.KindRecall:
		return true
	}
	h := mail.ParseHeaders(m.Headers)
	return len(h.Values("X-Autoreply")) > 0 || len(h.Values("X-Autorespond")) > 0
}

var trivialReplies = map[string]bool{
	"+1": true, "thanks": true, "thank you": true, "merci": true, "ok": true, "okay": true,
	"noted": true, "bien recu": true, "received": true, "recu": true, "👍": true,
}

func trivial(body string) bool {
	// Mail clients capitalize acknowledgements ("Thanks", "Noted"); the
	// table is lowercase, so the comparison must fold case too.
	return trivialReplies[strings.ToLower(strings.TrimSpace(body))]
}

func instant(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(mail.TimeLayout)
}
