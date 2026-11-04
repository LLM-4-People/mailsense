package evidence

func init() {
	register(Signal{Name: "importance_mark", Version: 1, Type: TypeEnum, Values: importanceValues[:3], Causal: false, Group: GroupOwner,
		Summary: true,
		Doc:     "The message's importance property now (MAPI PidTagImportance: 0 low, 1 normal, 2 high): set from the sender's headers on delivery, but the owner can change it on a received message (Outlook: Edit Message; inbox rules: MarkImportance). NOT causal: the owner's later action; not applicable when the source has none.",
		Extract: func(in *Input) Value {
			if i := in.Msg.Importance; i.Valid && i.N >= 0 && i.N <= 2 {
				return Str(importanceValues[i.N])
			}
			return NotApplicable(ReasonNone)
		}})
}
