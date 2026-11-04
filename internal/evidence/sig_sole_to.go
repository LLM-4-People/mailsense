package evidence

import "github.com/LLM-4-People/mailsense/internal/mail"

func init() {
	register(Signal{Name: "sole_to", Version: 1, Type: TypeBool, Needs: []string{"recipients"}, Causal: true, Group: GroupRecipients,
		Doc: "The owner is the only To recipient (written to them alone).",
		Extract: func(in *Input) Value {
			return received(in, func() Value {
				n, owner := 0, false
				for _, r := range in.Msg.Recipients {
					if r.Kind == mail.To {
						n++
						owner = owner || in.Owner.Owns(r.Address)
					}
				}
				return Bool(n == 1 && owner)
			})
		}})
}
