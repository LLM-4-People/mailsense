package evidence

import "time"

func init() {
	register(Signal{Data: contextData(), Summary: true, Name: "first_time_sender", Version: 2, Type: TypeEnum, Values: []string{"known", "new_address", "new_address_new_org"},
		Causal: true, Context: true, Group: GroupHistory, Settings: contextSettings(),
		Doc: "Whether the owner had exchanged mail with this sender before: known (the address), new_address (a new address of a known organisation, or of a shared free-mail domain), new_address_new_org (nobody of that organisation either).",
		Extract: func(in *Input) Value {
			return fromSender(in, func(addr string, at time.Time) Value {
				if s := in.history().AddressExchange(addr, at); s.Received+s.Sent > 0 {
					return Str("known")
				}
				key, isAddress := orgKey(in, addr)
				if isAddress {
					return Str("new_address")
				}
				if s := in.history().OrganisationExchange(key, at); s.Received+s.Sent > 0 {
					return Str("new_address")
				}
				return Str("new_address_new_org")
			})
		}})
}
