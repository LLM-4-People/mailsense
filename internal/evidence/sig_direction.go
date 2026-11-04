package evidence

// ReasonNoVerdict: scope has not judged the message yet.
const ReasonNoVerdict = "no_verdict"

func init() {
	register(Signal{Summary: true, Name: "direction", Version: 1, Type: TypeEnum, Values: []string{"received", "sent_by_owner"}, Causal: true,
		Group: GroupRecipients, Doc: "Whether the owner sent the message (scope: sender or sent on the owner's behalf) or received it.",
		Extract: func(in *Input) Value {
			switch {
			case in.Verdict == "":
				return Missing(ReasonNoVerdict)
			case in.SentByOwner():
				return Str("sent_by_owner")
			}
			return Str("received")
		}})
}
