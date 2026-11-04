package evidence

import "time"

func init() {
	register(Signal{Data: contextData(), Summary: true, Name: "reply_latency_band", Version: 2, Type: TypeInt, Causal: true, Context: true, Group: GroupHistory,
		Settings: contextSettings(settingBuckets),
		Doc:      "The band (index into threads.response_buckets: 0 fastest) of the owner's median response time to this sender before this message; not applicable before any reply.",
		Extract: func(in *Input) Value {
			return fromSender(in, func(addr string, at time.Time) Value {
				p, ok := in.history().Sender(addr, at)
				if !ok || p.Bucket < 0 {
					return NotApplicable(ReasonNone)
				}
				return Int(p.Bucket)
			})
		}})
}
