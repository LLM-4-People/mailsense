package evidence

import "github.com/LLM-4-People/mailsense/internal/mail"

func init() {
	register(Signal{Data: []string{DataMail}, Name: "message_kind", Version: 1, Type: TypeEnum, AnyOf: []string{"message_class", "headers"}, Causal: true,
		Values: []string{string(mail.KindOrdinary), string(mail.KindMeetingRequest), string(mail.KindMeetingCancel),
			string(mail.KindMeetingResponse), string(mail.KindDeliveryReport), string(mail.KindReadReceipt), string(mail.KindAutoReply),
			string(mail.KindRecall)},
		Group: GroupHeaders,
		Doc:   "What the item is beyond a message (mail.KindOf, kinds.json): from its message class, multipart/report type, calendar METHOD and Auto-Submitted keyword.",
		Extract: func(in *Input) Value {
			return Str(string(mail.KindOf(in.Msg)))
		}})
}
