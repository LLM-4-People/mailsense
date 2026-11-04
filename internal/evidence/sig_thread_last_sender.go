package evidence

import "github.com/LLM-4-People/mailsense/internal/threads"

func init() {
	register(Signal{Data: contextData(), Settings: contextSettings(), Name: "thread_last_sender", Version: 2, Type: TypeEnum, Values: []string{"none", "owner", "same_sender", "other"},
		Causal: true, Context: true, Group: GroupThread,
		Doc: "Who wrote the message just before this one in its conversation: the owner, this message's sender, someone else, or none.",
		Extract: func(in *Input) Value {
			return thread(in, func(self *threads.Message, earlier []*threads.Message) Value {
				if len(earlier) == 0 {
					return Str("none")
				}
				prev := earlier[len(earlier)-1]
				switch {
				case sent(prev):
					return Str("owner")
				case prev.Sender == self.Sender:
					return Str("same_sender")
				}
				return Str("other")
			})
		}})
}
