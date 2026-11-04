package evidence

import "github.com/LLM-4-People/mailsense/internal/mail"

func init() {
	register(Signal{Data: []string{DataPSL}, Name: "external_recipients", Version: 1, Type: TypeInt, Needs: []string{"recipients"}, Causal: true,
		Group: GroupRecipients, Settings: []string{mail.SettingSharedDomains},
		Doc: "To and Cc recipients outside the owners' organisations (not an owner, not a directory name, a domain that is not one of the owners' organisations).",
		Extract: func(in *Input) Value {
			n := 0
			for _, r := range in.Msg.Recipients {
				if r.Kind == mail.Bcc || in.Owner.Owns(r.Address) || mail.IsLegacyDN(r.Address) || mail.RegistrableDomain(r.Address) == "" {
					continue
				}
				if !in.env.orgs[in.env.set.Domains.Of(r.Address)] {
					n++
				}
			}
			return Int(n)
		}})
}
