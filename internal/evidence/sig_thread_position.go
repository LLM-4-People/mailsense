package evidence

import "github.com/LLM-4-People/mailsense/internal/threads"

func init() {
	register(Signal{Data: contextData(), Settings: contextSettings(), Name: "thread_position", Version: 2, Type: TypeInt, Causal: true, Context: true, Group: GroupThread,
		Doc: "Messages of the conversation dated before this one, as the conversation stood at this message's own instant (0: it starts the thread; a later message joining two conversations never changes it); conversations are built from the causal view: the owner-scope mail of every folder but drafts, wherever it is filed now (threads.CausalRoles).",
		Extract: func(in *Input) Value {
			return thread(in, func(_ *threads.Message, earlier []*threads.Message) Value { return Int(len(earlier)) })
		}})
}
