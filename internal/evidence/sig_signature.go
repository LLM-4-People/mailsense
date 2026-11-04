package evidence

import "github.com/LLM-4-People/mailsense/internal/mail"

func init() {
	register(Signal{Data: []string{DataMail}, Unsure: map[string][]string{"none": nil}, Name: "signature", Version: 1, Type: TypeEnum, Causal: true, Group: GroupContent,
		Values: []string{mail.SignatureNone, mail.SignatureDelimiter, mail.SignatureContacts},
		Doc:    "How the message's own text ends, by structure (mail.Signature): an RFC 3676 \"-- \" delimiter, a closing paragraph of contact lines (phone, address, link), or none.",
		Extract: func(in *Input) Value {
			sig := in.Signature()
			return Str(sig).Cut(in.BodyCut() && sig == mail.SignatureNone)
		}})
}
