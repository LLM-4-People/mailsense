package evidence

import "github.com/LLM-4-People/mailsense/internal/mail"

func init() {
	register(Signal{Data: []string{DataPSL}, Name: "name_address", Version: 1, Type: TypeEnum, Values: []string{"none", "same_domain", "other_domain"}, Causal: true,
		Group: GroupHeaders,
		Doc:   "An address written inside the sender's display name, against the sender's own address: other_domain is the classic disguise (\"ceo@example.com\" <x@example.net>); none when the name holds no address.",
		Extract: func(in *Input) Value {
			addrs := addressesIn(in.Msg.SenderName)
			if len(addrs) == 0 {
				return Str("none")
			}
			if mail.RegistrableDomain(in.Msg.SenderAddress) == "" {
				return NotApplicable(ReasonNoSenderDomain)
			}
			for _, a := range addrs {
				if relation(a, in.Msg.SenderAddress) == "other_domain" {
					return Str("other_domain")
				}
			}
			return Str("same_domain")
		}})
}
