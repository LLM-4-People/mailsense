package evidence

import "github.com/LLM-4-People/mailsense/internal/mail"

func init() {
	register(Signal{Data: []string{DataPSL}, Unsure: map[string][]string{"none": nil}, Name: "reply_to", Version: 1, Type: TypeEnum, Values: []string{"none", "same_address", "same_domain", "other_domain"},
		Needs: []string{"headers"}, Causal: true, Group: GroupHeaders,
		Doc: "Where replies go (Reply-To, RFC 5322 §3.6.2) against the From address: same_address, same_domain, other_domain (the most distant of its addresses), none without the header.",
		Extract: func(in *Input) Value {
			if mail.RegistrableDomain(in.Msg.SenderAddress) == "" {
				return NotApplicable(ReasonNoSenderDomain)
			}
			vals, missing, ok := in.Header(hdrReplyTo)
			if !ok {
				return missing
			}
			best := "none"
			for _, v := range vals {
				for _, a := range addressesIn(v) {
					if r := relation(a, in.Msg.SenderAddress); best == "none" || rank(r) > rank(best) {
						best = r
					}
				}
			}
			if best == "none" {
				return Str(best).Cut(in.HeadersCut())
			}
			return Str(best)
		}})
}

func rank(r string) int {
	switch r {
	case "same_address":
		return 1
	case "same_domain":
		return 2
	case "other_domain":
		return 3
	}
	return 0
}
