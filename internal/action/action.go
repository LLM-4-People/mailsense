// Package action is the one owner-facing classification of what a message
// asks of the mailbox owner and who is waiting. It is pure: the classifier
// signals and the structural thread state are its only inputs.
//
// The package exists because six screens used to answer nearby questions from
// different rules. It does not replace threadintel: that package owns the
// structural last mover and the visibility of the evidence. This package owns
// the semantic consequence: the one primary action, the waiting direction and
// the due source. A surface that needs "what should I do" or "what is waiting"
// reads this result instead of re-deriving it.
//
// Automated bulk mail never creates an owner action, even when it contains a
// call to action; the suspicious judgment still overrides that veto, so
// phishing inside a bulk category remains a threat. A classifier deadline is
// a proximity level, not a date: the result names the source so a surface
// never prints a deadline the mailbox does not store.
package action

import (
	"slices"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

// Primary is the one action a message asks of the owner.
type Primary string

// Owner-facing actions. The precedence between them is owned by primaryFor,
// not by this declaration order; Read means no action.
const (
	Read     Primary = "read"
	Reply    Primary = "reply"
	Decide   Primary = "decide"
	Pay      Primary = "pay"
	Schedule Primary = "schedule"
	Send     Primary = "send"
	Do       Primary = "do"
	Verify   Primary = "verify"
	FollowUp Primary = "follow_up"
	Wait     Primary = "wait"
)

// OwnerAction reports whether the primary action is work the owner must do.
// It excludes Read and Wait; Decide and Reply are owner actions too, so a
// commitment source can use one predicate for every kind.
func (p Primary) OwnerAction() bool {
	switch p {
	case Reply, Decide, Pay, Schedule, Send, Do, Verify, FollowUp:
		return true
	default:
		return false
	}
}

// Waiting is who must move for the conversation to progress.
type Waiting string

// Waiting directions.
const (
	WaitingNone    Waiting = "none"
	WaitingOwner   Waiting = "owner"
	WaitingOther   Waiting = "other"
	WaitingUnknown Waiting = "unknown"
)

// DueSource says where the time pressure came from.
type DueSource string

// Due sources.
const (
	DueNone       DueSource = ""
	DueStored     DueSource = "stored"     // a follow-up date the mailbox stores
	DueClassifier DueSource = "classifier" // the deadline score, never an exact date
)

// Class is the deterministic action classification of one message in its
// thread. Reasons are stable evidence codes, never prose.
type Class struct {
	Primary   Primary   `json:"primary"`
	Waiting   Waiting   `json:"waiting"`
	DueSource DueSource `json:"due_source,omitempty"`
	DueLevel  int       `json:"due_level,omitempty"`
	Reasons   []string  `json:"reasons,omitempty"`
}

// Signals is the thresholded classifier view the action rules read.
type Signals struct {
	Urgency        int
	ReplyNeeded    int
	Suspicious     int
	DecisionNeeded int
	Deadline       int
	Effort         int
	RequestCount   int
	IsQuestion     int
	CallToAction   int
	InfoSufficient int
	Category       string
	Topic          string
	Subtopic       string
	AsksFor        string
	Stance         string
	Relationship   string
}

// The signal levels that make a message urgent, deadline-bound or work. They
// are one rule: the ranking's filters, the ranking's reasons, the classifier's
// due source and the badge surfaces all classify the same stored levels, so a
// new threshold cannot reach one surface and miss another.
const (
	UrgentLevel   = 3
	DeadlineLevel = 3
	WorkloadLevel = 2
)

// Bands is the level half of the signal record: the four levels the band rule
// reads. It is a separate value so the ranking can ask the rule without
// converting its whole stored record.
type Bands struct {
	Urgency      int
	Deadline     int
	Effort       int
	RequestCount int
}

// UrgencyHigh reports the classifier's top urgency level. It is the ranking's
// "urgent" reason, which says the message itself is pressing.
func (b Bands) UrgencyHigh() bool { return b.Urgency >= UrgentLevel }

// Urgent is the "urgent" filter bucket: pressing now, either because the
// message is urgent or because a deadline is at the top level. It is the
// composition of the two reasons, so the bucket cannot disagree with them.
func (b Bands) Urgent() bool { return b.UrgencyHigh() || b.DeadlineSoon() }

// DeadlineSoon reports the classifier's top deadline level. It is a
// proximity, not a stored date.
func (b Bands) DeadlineSoon() bool { return b.Deadline >= DeadlineLevel }

// Workload reports work that costs real effort or asks several things.
func (b Bands) Workload() bool { return b.Effort >= WorkloadLevel || b.RequestCount >= WorkloadLevel }

// Bands is this record's levels.
func (s Signals) Bands() Bands {
	return Bands{Urgency: s.Urgency, Deadline: s.Deadline, Effort: s.Effort, RequestCount: s.RequestCount}
}

// Input is one message's evidence. Thread is the structural resolver's
// assessment; the zero value means no thread evidence and lets the
// message-level rules stand. StoredDue means the mailbox stores a follow-up
// date for this message or conversation. MessageID lets the classifier
// suppress history rows: only the message a thread still points at owes the
// action.
type Input struct {
	MessageID int64
	Signals   Signals
	Thread    threadintel.Assessment
	StoredDue bool
}

// Classify applies the one precedence table. The structural thread truth is
// terminal where it is decisive: a thread someone already answered cannot
// still ask the owner to reply, and a history row cannot duplicate the action
// of the current one. A thread that needs no reply is not a thread that needs
// no work: an invoice can still ask the owner to pay, so the message-level
// rules keep running and the waiting direction follows the concrete action.
func Classify(in Input) Class {
	thread := in.Thread
	// The bulk predicate is one fact about the message. Compute it once and
	// pass it to the two rules that read it instead of re-deriving it.
	bulk := Bulk(in.Signals)

	// Only the message a thread still points at can owe an action. This runs
	// before the state switch so an older inbound row in an awaiting_owner
	// thread never duplicates the current obligation.
	if thread.State == threadintel.AwaitingOwner && thread.ReplyToMessageID != 0 &&
		in.MessageID != 0 && thread.ReplyToMessageID != in.MessageID {
		return Class{Primary: Read, Waiting: WaitingNone, Reasons: []string{"thread_context"}}
	}
	if thread.State != threadintel.Unknown && in.MessageID != 0 && thread.LastMessageID != 0 &&
		in.MessageID != thread.LastMessageID {
		return Class{Primary: Read, Waiting: WaitingNone, Reasons: []string{"thread_history"}}
	}

	switch thread.State {
	case threadintel.AnsweredByOwner, threadintel.AnsweredByOther:
		return Class{Primary: Read, Waiting: WaitingNone, Reasons: []string{"thread_answered"}}
	case threadintel.AwaitingOther:
		// threadintel only reports this state when the owner moved last with
		// a real request. A closing thank-you is not waiting.
		return withDue(Class{Primary: Wait, Waiting: WaitingOther, Reasons: []string{"thread_waiting_other"}}, in, bulk)
	case threadintel.NoAction:
		// CC-only, a closing owner note, automated mail and auto-replies owe
		// nothing. A plain no-reply message may still carry a concrete ask
		// (pay, approve, send a document), which the rules below classify.
		switch thread.Reason {
		case threadintel.ReasonCcOnly, threadintel.ReasonOwnerLastClosed,
			threadintel.ReasonAutoReply, threadintel.ReasonAutomated:
			return Class{Primary: Read, Waiting: WaitingNone, Reasons: []string{"no_action"}}
		}
	}

	primary, reasons := primaryFor(in.Signals, bulk)
	waiting := waitingFor(primary, thread)
	out := Class{Primary: primary, Waiting: waiting, Reasons: reasons}
	out = withDue(out, in, bulk)

	// A thread with incomplete evidence cannot claim the owner is waiting on
	// the other side. Keep the message-level action, but make the direction
	// explicit.
	if thread.State == threadintel.Unknown {
		out.Waiting = WaitingUnknown
	}
	return out
}

// Automated reports machine-generated bulk mail from the two independent
// answers: the subject category is one of the bulk classes, or the sender
// relationship is an automated service. An absent or abstained category is
// unknown, not human; it is only automated when the relationship says so.
// Such mail never creates an owner action, even when its text asks for a
// click or contains a question. It is the one bulk classification the action,
// ranking and thread rules share; Bulk applies the threat override.
func Automated(category, relationship string) bool {
	switch category {
	case "spam", "marketing", "notifications":
		return true
	default:
		return relationship == "automated_service"
	}
}

// Bulk reports whether one message is automated bulk mail that owes the owner
// nothing. The suspicious judgment overrides the veto: phishing sent inside a
// bulk category is a threat, not bulk mail to ignore.
func Bulk(sig Signals) bool {
	return sig.Suspicious != 1 && Automated(sig.Category, sig.Relationship)
}

// withDue attaches the one due source and turns a purely informational row
// with a stored follow-up into a follow-up action. A classifier deadline is
// only a level; it never becomes a date. Automated bulk mail never gets a due
// source, so urgency or a deadline cannot pull it into the work list. bulk is
// Classify's one bulk predicate for this input.
func withDue(c Class, in Input, bulk bool) Class {
	if bulk {
		return c
	}
	if in.StoredDue {
		c.DueSource = DueStored
		if c.Primary == Read {
			c.Primary = FollowUp
			c.Waiting = WaitingOwner
		}
		c.Reasons = appendOnce(c.Reasons, "due")
		return c
	}
	if in.Signals.Bands().DeadlineSoon() {
		c.DueSource = DueClassifier
		c.DueLevel = in.Signals.Deadline
		c.Reasons = appendOnce(c.Reasons, "classifier_deadline")
		// A date the classifier only estimates never turns a real action
		// into a different one; it turns an informational row into a
		// follow-up so a deadline cannot disappear from the work list.
		if c.Primary == Read {
			c.Primary = FollowUp
			c.Waiting = WaitingOwner
		}
	}
	return c
}

// primaryFor is the message-level precedence table. A threat verdict first,
// then the bulk-mail veto, then a verdict, then the named ask, then a generic
// reply, then a call to action. The named ask is more precise than "reply":
// "pay this invoice and confirm" is a payment, not a message to answer.
// Automated bulk mail is read-only, even when its text asks for a click. A
// security category is a subject, not a threat: it creates Verify only when
// the suspicious judgment is positive. bulk is Classify's one bulk predicate
// for this input.
func primaryFor(sig Signals, bulk bool) (Primary, []string) {
	reasons := make([]string, 0, 5)
	if sig.Suspicious == 1 {
		reasons = append(reasons, "security")
		return Verify, reasons
	}
	if bulk {
		reasons = append(reasons, "automated")
		return Read, reasons
	}
	if sig.DecisionNeeded == 1 {
		reasons = append(reasons, "decision")
		return Decide, reasons
	}
	// The dominant ask is one table: AskAction decides the owner-facing action,
	// threadintel.ClassifyAsk decides whether the ask is recognized at all and
	// whether it is anything to act on. The ranking reads the same table for
	// its "concrete ask" entry rule.
	if kind := threadintel.ClassifyAsk(sig.AsksFor); kind != threadintel.AskNone {
		primary, _ := AskAction(sig.AsksFor)
		if kind == threadintel.AskFYI {
			reasons = append(reasons, "fyi")
		} else {
			reasons = append(reasons, "ask:"+strings.ToLower(strings.TrimSpace(sig.AsksFor)))
		}
		return primary, reasons
	}
	if sig.ReplyNeeded == 1 {
		reasons = append(reasons, "reply")
		return Reply, reasons
	}
	if sig.CallToAction == 1 {
		reasons = append(reasons, "cta")
		return Do, reasons
	}
	return Read, reasons
}

// AskAction maps the profile's dominant ask to the one owner-facing action it
// implies, and reports whether that is concrete work (a decision, a document,
// money, time or a task) rather than a reply or an FYI. threadintel owns the
// vocabulary and the structural meaning; this is the owner-facing consequence,
// and the ranking's "an explicit ask always enters" rule reads it too, so the
// two can never disagree about which asks are work.
func AskAction(asksFor string) (Primary, bool) {
	switch threadintel.ClassifyAsk(asksFor) {
	case threadintel.AskReply:
		return Reply, false
	case threadintel.AskFYI:
		return Read, false
	case threadintel.AskWork:
		switch threadintel.AskOption(asksFor) {
		case threadintel.AskPaymentMoney:
			return Pay, true
		case threadintel.AskMeetingAvailability:
			return Schedule, true
		case threadintel.AskInformationDocument:
			return Send, true
		case threadintel.AskTaskWork:
			return Do, true
		case threadintel.AskApprovalDecision:
			return Decide, true
		}
	}
	return Read, false
}

// waitingFor maps the primary action and the thread state to one direction.
// A terminal state is decisive; otherwise an owner action means the owner is
// the one who must move.
func waitingFor(primary Primary, thread threadintel.Assessment) Waiting {
	switch thread.State {
	case threadintel.AwaitingOwner:
		return WaitingOwner
	case threadintel.AwaitingOther:
		return WaitingOther
	case threadintel.AnsweredByOwner, threadintel.AnsweredByOther:
		return WaitingNone
	case threadintel.NoAction:
		// No reply is owed. A concrete ask still means someone is waiting
		// on the owner; an informational row means nobody is.
		if primary.OwnerAction() {
			return WaitingOwner
		}
		return WaitingNone
	case threadintel.Unknown:
		return WaitingUnknown
	}
	if primary == Wait {
		return WaitingOther
	}
	if primary.OwnerAction() {
		return WaitingOwner
	}
	return WaitingNone
}

func appendOnce(list []string, value string) []string {
	if slices.Contains(list, value) {
		return list
	}
	return append(list, value)
}
