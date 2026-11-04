package evidence

import "github.com/LLM-4-People/mailsense/internal/mail"

func init() {
	register(Signal{Name: "addressed_to_me", Version: 1, Type: TypeEnum, Values: []string{"to", "cc", "bcc", "not_addressed"},
		Needs: []string{"recipients"}, Causal: true, Group: GroupRecipients,
		Doc: "How the owner received the message: in To, in Cc, in Bcc (a recipient or the source's addressed-to-me flag), or not addressed at all (a list, an alias, a forward).",
		Extract: func(in *Input) Value {
			return received(in, func() Value {
				best := ""
				for _, r := range in.Msg.Recipients {
					if in.Owner.Owns(r.Address) && (best == "" || r.Kind == mail.To || r.Kind == mail.Cc && best == string(mail.Bcc)) {
						best = string(r.Kind)
					}
				}
				if best == "" && in.Caps.AddressedToMe {
					switch in.Msg.AddressedToMe {
					case mail.AddressedTo, mail.AddressedCc, mail.AddressedBcc:
						best = in.Msg.AddressedToMe
					}
				}
				if best == "" {
					best = "not_addressed"
				}
				return Str(best)
			})
		}})
}
