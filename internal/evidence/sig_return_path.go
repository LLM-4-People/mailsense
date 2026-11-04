package evidence

import (
	"strings"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

func init() {
	register(Signal{Data: []string{DataPSL}, Unsure: map[string][]string{"none": nil}, Name: "return_path", Version: 1, Type: TypeEnum, Values: []string{"none", "null", "same_domain", "other_domain"},
		Needs: []string{"headers"}, Causal: true, Group: GroupHeaders,
		Doc: "The envelope sender (Return-Path, RFC 5321 §4.4) against the From address's registrable domain: same_domain, other_domain (a sending service or a forgery), null (<>: a bounce), none without the header.",
		Extract: func(in *Input) Value {
			if mail.RegistrableDomain(in.Msg.SenderAddress) == "" {
				return NotApplicable(ReasonNoSenderDomain)
			}
			vals, missing, ok := in.Header(hdrReturnPath)
			if !ok {
				return missing
			}
			if len(vals) == 0 {
				return Str("none").Cut(in.HeadersCut())
			}
			addrs := addressesIn(vals[0])
			if len(addrs) == 0 {
				if strings.TrimSpace(vals[0]) == "<>" {
					return Str("null")
				}
				return Str("none")
			}
			if relation(addrs[0], in.Msg.SenderAddress) == "other_domain" {
				return Str("other_domain")
			}
			return Str("same_domain")
		}})
}
