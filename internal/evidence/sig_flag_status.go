package evidence

func init() {
	register(Signal{Summary: true, Name: "flag_status", Version: 1, Type: TypeEnum, Values: []string{"none", "marked", "complete", "other"},
		Needs: []string{"flags"}, Group: GroupOwner,
		Doc: "The owner's follow-up flag (MS-OXOFLAG: none, marked, complete). NOT causal: the owner's later action.",
		Extract: func(in *Input) Value {
			f := in.Msg.FlagStatus
			if !f.Valid {
				return Str("none") // MAPI omits the property of an item never flagged
			}
			if s, ok := flagStates[f.N]; ok {
				return Str(s)
			}
			return Str("other")
		}})
}
