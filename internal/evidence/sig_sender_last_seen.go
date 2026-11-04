package evidence

import "time"

func init() {
	register(Signal{Data: contextData(), Settings: contextSettings(), Summary: true, Name: "sender_last_seen_days", Version: 2, Type: TypeFloat, Causal: true, Context: true, Group: GroupHistory,
		Doc: "Days since the latest earlier message exchanged with this sender's address (either way); not applicable for a first contact.",
		Extract: func(in *Input) Value {
			return fromSender(in, func(addr string, at time.Time) Value {
				s := in.history().AddressExchange(addr, at)
				if s.Last.IsZero() {
					return NotApplicable(ReasonFirst)
				}
				return Num(days(at.Sub(s.Last)))
			})
		}})
}
