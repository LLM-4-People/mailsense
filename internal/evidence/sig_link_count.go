package evidence

import "github.com/LLM-4-People/mailsense/internal/mail"

func init() {
	register(Signal{Data: []string{DataMail}, Bound: AtLeast, Name: "link_count", Version: 1, Type: TypeInt, Causal: true, Group: GroupContent,
		Doc:     "Links in the message's own text (subject and cleaned body; mail.Links).",
		Extract: func(in *Input) Value { return Int(mail.Links(in.Own())).Cut(in.BodyCut()) }})
}
