package evidence

import "time"

func init() {
	register(Signal{Data: contextData(), Settings: contextSettings(), Summary: true, Name: "sender_received_before", Version: 2, Type: TypeInt, Causal: true, Context: true, Group: GroupHistory,
		Doc: "Messages the owner received from this sender's address before this one (the causal view: owner-scope mail of every folder but drafts, wherever it is filed now).",
		Extract: func(in *Input) Value {
			return fromSender(in, func(addr string, at time.Time) Value { return Int(in.history().AddressExchange(addr, at).Received) })
		}})
}
