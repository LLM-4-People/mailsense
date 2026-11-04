package evidence

import "time"

func init() {
	register(Signal{Data: contextData(), Settings: contextSettings(), Summary: true, Name: "owner_sent_before", Version: 2, Type: TypeInt, Causal: true, Context: true, Group: GroupHistory,
		Doc: "Messages the owner sent to this sender's address (To or Cc) before this one.",
		Extract: func(in *Input) Value {
			return fromSender(in, func(addr string, at time.Time) Value { return Int(in.history().AddressExchange(addr, at).Sent) })
		}})
}
