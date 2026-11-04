package scope

import (
	"slices"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Verdict is the scope of one message.
type Verdict string

// Verdicts. Only owner mail feeds behaviour learning and statistics; shared
// and unattributed mail is kept for search and never deleted.
const (
	Owner        Verdict = "owner"
	Shared       Verdict = "shared"
	Unattributed Verdict = "unattributed"
)

// allVerdicts is the one list of verdicts; Valid reads it.
var allVerdicts = []Verdict{Owner, Shared, Unattributed}

// Valid reports whether v is one of the three verdicts.
func (v Verdict) Valid() bool { return slices.Contains(allVerdicts, v) }

// Reason names the rule that produced a verdict (shown in the UI).
type Reason string

// Reasons, one per rule of Decide.
const (
	ReasonSentByOwner     Reason = "sent_by_owner"         // sender or sent-on-behalf is the owner
	ReasonSentByOther     Reason = "sent_by_other"         // a Sent-role item someone else sent
	ReasonSentNoSender    Reason = "sent_without_sender"   // a Sent-role item without a sender
	ReasonRecipientMatch  Reason = "recipient_is_owner"    // To/Cc/Bcc holds an owner address
	ReasonAddressedFlag   Reason = "addressed_to_me_flag"  // the source's addressed-to-me flag
	ReasonDeliveredTo     Reason = "delivered_to_owner"    // Delivered-To / X-Original-To names the owner
	ReasonMailingList     Reason = "mailing_list_delivery" // list mail (List-Id) delivered to this mailbox
	ReasonNotAddressed    Reason = "not_addressed_flag"
	ReasonRecipientsOther Reason = "recipients_are_others"
	ReasonNoRecipients    Reason = "no_recipients"
)

// allReasons is the one list of reasons; Valid reads it. The AST guard in
// verdict_test.go keeps it in step with the constants above, so a rule added to
// Decide without being listed here fails the test instead of every write.
var allReasons = []Reason{
	ReasonSentByOwner, ReasonSentByOther, ReasonSentNoSender, ReasonRecipientMatch, ReasonAddressedFlag,
	ReasonDeliveredTo, ReasonMailingList, ReasonNotAddressed, ReasonRecipientsOther, ReasonNoRecipients,
}

// Valid reports whether r is a rule of Decide.
func (r Reason) Valid() bool { return slices.Contains(allReasons, r) }

// Delivery headers: the envelope recipient the receiving server recorded
// (protocol names, not language data).
var deliveryHeaders = []string{"Delivered-To", "X-Original-To", "Envelope-To"}

// Evidence is what Decide needs from a message.
type Evidence struct {
	Role             mail.FolderRole
	SenderAddress    string
	SentRepresenting string
	AddressedToMe    string   // "" when the source has no such flag
	Recipients       []string // to, cc and bcc addresses
	DeliveredTo      []string // envelope recipients from the delivery headers
	ListID           bool     // the message carries a List-Id header (RFC 2919)
}

// EvidenceOf extracts the evidence of a message.
func EvidenceOf(m *mail.Message) Evidence {
	ev := Evidence{Role: m.FolderRole, SenderAddress: m.SenderAddress, SentRepresenting: m.SentRepresentingAddress,
		AddressedToMe: m.AddressedToMe}
	for _, r := range m.Recipients {
		ev.Recipients = append(ev.Recipients, r.Address)
	}
	ev.AddHeaders(m.Headers)
	return ev
}

// AddHeaders reads the delivery and list evidence of a raw header block.
func (ev *Evidence) AddHeaders(block string) {
	h := mail.ParseHeaders(block)
	for _, name := range deliveryHeaders {
		for _, v := range h.Values(name) {
			ev.DeliveredTo = append(ev.DeliveredTo, mail.SplitAddresses(strings.Trim(v, "<>"))...)
		}
	}
	ev.ListID = h.Value("List-Id") != ""
}

// Decide applies the scope rules against a KNOWN store identity (a store
// without one gets no verdicts: the caller reports it). Own output first
// (sender or sent-on-behalf is the owner), then Sent-role items of
// others, then incoming evidence: an owner recipient, the source's
// addressed-to-me flag, the envelope recipient; then the source's explicit
// "not addressed to me" flag, which outranks the mere presence of a
// List-Id (list mail forwarded through someone else's alias is not the
// owner's); then list delivery and the recipients.
func Decide(ev Evidence, id Identity) (Verdict, Reason) {
	if id.Owns(ev.SenderAddress) || id.Owns(ev.SentRepresenting) {
		return Owner, ReasonSentByOwner
	}
	if ev.Role == mail.RoleSent {
		if strings.TrimSpace(ev.SenderAddress) == "" {
			return Unattributed, ReasonSentNoSender
		}
		return Shared, ReasonSentByOther
	}
	seen := false
	for _, r := range ev.Recipients {
		if mail.NormalizeAddress(r) == "" {
			continue
		}
		seen = true
		if id.Owns(r) {
			return Owner, ReasonRecipientMatch
		}
	}
	flag := strings.ToLower(strings.TrimSpace(ev.AddressedToMe))
	switch flag {
	case mail.AddressedTo, mail.AddressedCc, mail.AddressedBcc:
		return Owner, ReasonAddressedFlag
	}
	for _, d := range ev.DeliveredTo {
		if id.Owns(d) {
			return Owner, ReasonDeliveredTo
		}
	}
	if flag == mail.AddressedNo {
		return Shared, ReasonNotAddressed
	}
	if ev.ListID {
		return Owner, ReasonMailingList
	}
	if !seen {
		return Unattributed, ReasonNoRecipients
	}
	return Shared, ReasonRecipientsOther
}
