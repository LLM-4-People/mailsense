package evidence

func init() {
	register(Signal{Summary: true, Name: "last_verb", Version: 1, Type: TypeEnum, Values: []string{"none", "open", "reply", "reply_all", "forward", "other"},
		Needs: []string{"last_verb"}, Group: GroupOwner,
		Doc: "The owner's last action on the message (MS-OXOMSG PidTagLastVerbExecuted: open, reply, reply_all, forward, other; none when never set). NOT causal.",
		Extract: func(in *Input) Value {
			v := in.Msg.LastVerbExecuted
			if !v.Valid {
				return Str("none")
			}
			if s, ok := lastVerbs[v.N]; ok {
				return Str(s)
			}
			return Str("other")
		}})
}
