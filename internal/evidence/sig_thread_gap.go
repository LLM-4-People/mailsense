package evidence

import "github.com/LLM-4-People/mailsense/internal/threads"

func init() {
	register(Signal{Data: contextData(), Settings: contextSettings(), Name: "thread_gap_minutes", Version: 2, Type: TypeFloat, Causal: true, Context: true, Group: GroupThread,
		Doc: "Minutes since the previous message of the conversation (not applicable for its first message).",
		Extract: func(in *Input) Value {
			return thread(in, func(self *threads.Message, earlier []*threads.Message) Value {
				if len(earlier) == 0 {
					return NotApplicable(ReasonFirst)
				}
				return Num(self.At.Sub(earlier[len(earlier)-1].At).Minutes())
			})
		}})
}
