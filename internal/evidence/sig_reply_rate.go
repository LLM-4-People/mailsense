package evidence

import "time"

func init() {
	register(Signal{Data: contextData(), Settings: contextSettings(), Summary: true, Name: "reply_rate", Version: 2, Type: TypeFloat, Causal: true, Context: true, Group: GroupHistory,
		Doc: "Share of this sender's earlier messages the owner had answered by this message's instant (threads reply matching, causal); not applicable before any.",
		Extract: func(in *Input) Value {
			return fromSender(in, func(addr string, at time.Time) Value {
				p, ok := in.history().Sender(addr, at)
				if !ok || p.Received == 0 {
					return NotApplicable(ReasonFirst)
				}
				return Num(float64(p.Replied) / float64(p.Received))
			})
		}})
}
