package evidence

import "github.com/LLM-4-People/mailsense/internal/mail"

func init() {
	register(Signal{Data: []string{DataPSL}, Summary: true, Name: "sender_relation", Version: 1, Type: TypeEnum, Values: []string{"self", "internal", "external", "directory", "none"},
		Causal: true, Group: GroupRecipients, Settings: []string{mail.SettingSharedDomains},
		Doc: "The sender against the owner: self (the owner), internal (an organisation of the picked mailboxes' owners: registrable domain, shared free-mail domains excluded), external, directory (an Exchange distinguished name: an address of the owner's directory, no domain), none (no sender).",
		Extract: func(in *Input) Value {
			a := in.Msg.SenderAddress
			switch {
			case in.SentByOwner() || in.Owner.Owns(a):
				return Str("self")
			case mail.IsLegacyDN(a):
				return Str("directory")
			case mail.RegistrableDomain(a) == "":
				return Str("none")
			case in.env.orgs[in.env.set.Domains.Of(a)]:
				return Str("internal")
			}
			return Str("external")
		}})
}
