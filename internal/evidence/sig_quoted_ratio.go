package evidence

import "github.com/LLM-4-People/mailsense/internal/mail"

func init() {
	register(Signal{Data: []string{DataMail}, Name: "quoted_ratio", Version: 1, Type: TypeFloat, Causal: true, Group: GroupContent,
		Doc: "Share of the body that is not the message's own text (quoted replies, forwarded headers, signature: mail.CleanBody), in characters; not applicable for an empty body.",
		Extract: func(in *Input) Value {
			all := mail.Runes(mail.Strip(in.Msg.Body))
			if all == 0 {
				return NotApplicable(ReasonNone)
			}
			own := mail.Runes(in.Clean())
			return Num(max(0, float64(all-own)/float64(all))).Cut(in.BodyCut())
		}})
}
