package evidence

import "time"

func init() {
	register(Signal{Data: contextData(), Settings: contextSettings(), Summary: true, Name: "owner_share", Version: 2, Type: TypeFloat, Causal: true, Context: true, Group: GroupHistory,
		Doc: "The owner's share of the exchange with this sender before this message: sent to it over sent plus received (0: the owner only listens; not applicable before any exchange).",
		Extract: func(in *Input) Value {
			return fromSender(in, func(addr string, at time.Time) Value {
				s := in.history().AddressExchange(addr, at)
				if s.Received+s.Sent == 0 {
					return NotApplicable(ReasonFirst)
				}
				return Num(float64(s.Sent) / float64(s.Received+s.Sent))
			})
		}})
}
