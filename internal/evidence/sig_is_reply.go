package evidence

import (
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/threads"
)

func init() {
	register(Signal{Data: []string{DataMail}, Name: "is_reply", Version: 1, Type: TypeBool, Causal: true, Group: GroupThread,
		Settings: []string{mail.SettingSubjectPrefixes, mail.SettingSubjectPrefixesLearned, threads.SettingMaxPrefixes},
		Doc:      "The message answers or forwards another: it references a parent (In-Reply-To/References) or its subject starts with a reply or forward prefix (mail.SubjectPrefixes).",
		Extract: func(in *Input) Value {
			_, parents := mail.ThreadIDs(in.Msg)
			return Bool(len(parents) > 0 || in.env.set.Threads.Normalizer.HasPrefix(in.Msg.Subject))
		}})
}
