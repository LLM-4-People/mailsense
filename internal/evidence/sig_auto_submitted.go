package evidence

import "github.com/LLM-4-People/mailsense/internal/mail"

func init() {
	register(Signal{Data: []string{DataMail}, Unsure: map[string][]string{"none": nil}, Name: "auto_submitted", Version: 1, Type: TypeEnum, Values: autoSubmittedValues, Needs: []string{"headers"},
		Causal: true, Group: GroupHeaders,
		Doc: "The Auto-Submitted keyword (RFC 3834, RFC 5436): no (written by a person), auto-generated, auto-replied, auto-notified, other; none without the header.",
		Extract: func(in *Input) Value {
			return headerEnum(in, hdrAutoSubmitted, func(v string) string {
				return inVocabulary(mail.AutoSubmittedKeyword(v), autoSubmittedValues[1:5])
			})
		}})
}
