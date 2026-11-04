package evidence

import "github.com/LLM-4-People/mailsense/internal/mail"

func init() {
	register(Signal{Data: []string{DataMail}, Bound: AtLeast, Name: "question_marks", Version: 1, Type: TypeInt, Causal: true, Group: GroupContent,
		Doc:     "Question marks of any script (UCD: ? ？ ؟ ;…) in the message's own text.",
		Extract: func(in *Input) Value { return Int(mail.QuestionMarks(in.Own())).Cut(in.BodyCut()) }})
}
