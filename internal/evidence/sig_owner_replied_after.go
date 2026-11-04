package evidence

import "github.com/LLM-4-People/mailsense/internal/threads"

func init() {
	register(Signal{Data: contextData(), Settings: contextSettings(), Name: "owner_replied_after", Version: 2, Type: TypeBool, Causal: false, Context: true, Group: GroupThread,
		Doc: "The owner answered this message later (threads reply matching). NOT causal: an outcome for evaluation only, never a feature, fact or rule input.",
		Extract: func(in *Input) Value {
			if in.SentByOwner() {
				return NotApplicable(ReasonSent)
			}
			return thread(in, func(self *threads.Message, _ []*threads.Message) Value {
				_, ok := in.env.thread.an.MyReplies[self.RowID]
				return Bool(ok)
			})
		}})
}
