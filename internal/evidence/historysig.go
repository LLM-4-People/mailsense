package evidence

import (
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// fromSender answers a history signal about a received message's sender
// at the message's instant: NA for the owner's own messages, undated
// mail and mail without a sender address.
func fromSender(in *Input, fn func(addr string, at time.Time) Value) Value {
	at, dated := in.At()
	addr := mail.NormalizeAddress(in.Msg.SenderAddress)
	switch {
	case in.SentByOwner():
		return NotApplicable(ReasonSent)
	case !dated:
		return NotApplicable(ReasonNoDate)
	case addr == "":
		return NotApplicable(ReasonNone)
	}
	return fn(addr, at)
}

// orgKey is the organisation a sender's history is kept under: its
// organisation (mail.Domains), or the address itself for a shared
// free-mail domain (there the person is the correspondent).
func orgKey(in *Input, addr string) (key string, isAddress bool) {
	if o := in.env.set.Domains.Of(addr); o != "" {
		return o, false
	}
	return addr, true
}

func days(d time.Duration) float64 { return d.Hours() / 24 }
