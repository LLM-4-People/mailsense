package evidence

import "github.com/LLM-4-People/mailsense/internal/threads"

func init() {
	register(Signal{Data: contextData(), Settings: contextSettings(), Name: "owner_asked", Version: 2, Type: TypeEnum, Values: []string{"none", "asked", "not_asked"},
		Causal: true, Context: true, Group: GroupThread,
		Doc: "Whether the owner's latest earlier message in the conversation asked something: its own text (mail.CleanBody) holds a question mark of any script (UCD); none when the owner had not written.",
		Extract: func(in *Input) Value {
			return thread(in, func(_ *threads.Message, earlier []*threads.Message) Value {
				for i := len(earlier) - 1; i >= 0; i-- {
					if sent(earlier[i]) {
						if in.env.thread.asked[earlier[i].RowID] {
							return Str("asked")
						}
						return Str("not_asked")
					}
				}
				return Str("none")
			})
		}})
}
