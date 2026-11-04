package evidence

import "time"

func init() {
	register(Signal{Data: []string{DataMail, DataPSL, DataLocale}, Summary: true, Name: "lookalike_domain", Version: 2, Type: TypeEnum, Values: []string{LookNone, LookSkeleton, LookEdit},
		Causal: true, Context: true, Group: GroupHistory, Settings: contextSettings(settingMaxEdits, settingMinNameChars),
		Doc: "The sender's registrable domain imitates one the owner corresponds with (wrote to before this message, or the owner's own) without being it: skeleton (the same UTS #39 confusables skeleton, раураl.com for paypal.com), edit (within evidence.lookalike.max_edits edits, examp1e.com), none. Domains whose first label is shorter than evidence.lookalike.min_chars are never look-alikes.",
		Extract: func(in *Input) Value {
			return fromSender(in, func(addr string, at time.Time) Value {
				d := unicodeDomain(addr)
				if d == "" {
					return NotApplicable(ReasonNoSenderDomain)
				}
				return Str(in.env.look.at(d, at))
			})
		}})
}
