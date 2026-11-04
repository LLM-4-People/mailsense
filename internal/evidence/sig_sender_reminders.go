package evidence

import "github.com/LLM-4-People/mailsense/internal/threads"

func init() {
	register(Signal{Data: contextData(), Settings: contextSettings(), Name: "sender_reminders", Version: 2, Type: TypeInt, Causal: true, Context: true, Group: GroupThread,
		Doc: "Messages the same sender sent in the conversation right before this one with no answer from the owner in between: how many times they are writing again.",
		Extract: func(in *Input) Value {
			if in.SentByOwner() {
				return NotApplicable(ReasonSent)
			}
			return thread(in, func(self *threads.Message, earlier []*threads.Message) Value {
				n := 0
				for i := len(earlier) - 1; i >= 0 && !sent(earlier[i]) && earlier[i].Sender == self.Sender; i-- {
					n++
				}
				return Int(n)
			})
		}})
}
