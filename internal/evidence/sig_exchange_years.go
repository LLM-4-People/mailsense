package evidence

import "time"

func init() {
	register(Signal{Data: contextData(), Summary: true, Name: "exchange_years", Version: 2, Type: TypeInt, Causal: true, Context: true, Group: GroupHistory,
		Settings: contextSettings(),
		Doc:      "Calendar years (UTC) before this message in which the owner and the sender's organisation wrote to each other both ways (the address itself for a shared free-mail domain).",
		Extract: func(in *Input) Value {
			return fromSender(in, func(addr string, at time.Time) Value {
				key, isAddress := orgKey(in, addr)
				if isAddress {
					return Int(in.history().AddressExchange(key, at).Years)
				}
				return Int(in.history().OrganisationExchange(key, at).Years)
			})
		}})
}
