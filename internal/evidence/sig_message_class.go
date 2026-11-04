package evidence

import (
	"strings"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// CustomClass is how aggregates show a message class the protocol does not
// document (an organisation's or a vendor's form): its name never leaves.
const CustomClass = "custom"

func init() {
	register(Signal{Summary: true, Name: "message_class", Version: 1, Type: TypeCategory, Needs: []string{"message_class"}, Causal: true,
		Group: GroupHeaders, Shown: func(c string) string {
			if mail.DocumentedClass(c) {
				return c
			}
			return CustomClass
		},
		Doc: "The item's message class (MS-OXCMSG PidTagMessageClass: IPM.Note, IPM.Schedule.Meeting.Request, IPM.Note.Microsoft.Voicemail.UM…), lower case: a protocol value, kept as written.",
		Extract: func(in *Input) Value {
			if c := strings.ToLower(strings.TrimSpace(in.Msg.MessageClass)); c != "" {
				return Str(c)
			}
			return NotApplicable(ReasonNone)
		}})
}
