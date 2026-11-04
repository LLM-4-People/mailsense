package evidence

import "github.com/LLM-4-People/mailsense/internal/threads"

func init() {
	register(Signal{Data: contextData(), Settings: contextSettings(), Name: "back_and_forth", Version: 2, Type: TypeInt, Causal: true, Context: true, Group: GroupThread,
		Doc: "Changes of direction (owner to correspondent and back) in the conversation up to this message.",
		Extract: func(in *Input) Value {
			return thread(in, func(self *threads.Message, earlier []*threads.Message) Value {
				n := 0
				all := append(earlier[:len(earlier):len(earlier)], self)
				for i := 1; i < len(all); i++ {
					if sent(all[i]) != sent(all[i-1]) {
						n++
					}
				}
				return Int(n)
			})
		}})
}
