package evidence

import "github.com/LLM-4-People/mailsense/internal/mail"

// ReasonNoIdentity: the message's store has no owner identity (none
// reported, none declared): who it was addressed to cannot be told.
const ReasonNoIdentity = "no_owner_identity"

// toCc counts the To and Cc recipients.
func toCc(m *mail.Message) (to, cc int) {
	for _, r := range m.Recipients {
		switch r.Kind {
		case mail.To:
			to++
		case mail.Cc:
			cc++
		}
	}
	return to, cc
}

// received answers a signal about received mail: NA for the owner's own
// messages, Missing without an owner identity.
func received(in *Input, fn func() Value) Value {
	switch {
	case in.SentByOwner():
		return NotApplicable(ReasonSent)
	case !in.Owner.Known():
		return Missing(ReasonNoIdentity)
	}
	return fn()
}
