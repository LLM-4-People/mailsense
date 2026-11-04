package evidence

import "github.com/LLM-4-People/mailsense/internal/mail"

func init() {
	register(Signal{Data: []string{DataMail}, Bound: AtLeast, Name: "own_chars", Version: 1, Type: TypeInt, Causal: true, Group: GroupContent,
		Doc:     "Characters of the message's own body text (mail.CleanBody), in every script alike.",
		Extract: func(in *Input) Value { return Int(mail.Runes(in.Clean())).Cut(in.BodyCut()) }})
}
