package evidence

func init() {
	register(Signal{Data: textData(), Bound: AtMost, Name: "date_past_days", Version: 1, Type: TypeInt, Causal: true, Group: GroupContent, Settings: dateSettings,
		Doc: "Days from the message's day to the nearest full date the own text names strictly before it (1: the day before; positive: overdue); 0 when it names a full date but none before it, not applicable when it names none (the reason of an unresolved one when it names only such: ambiguous_order, calendar:<name>). A cut body may hide a nearer past date, so the reported nearest is at least as far back as the whole text's (a rule takes it only as a maximum: at_most).",
		Extract: func(in *Input) Value {
			return datesOf(in, false, func(ds dates) Value {
				if len(ds.days) == 0 {
					if len(ds.unresolved) > 0 {
						return NotApplicable(ds.unresolved[0])
					}
					return NotApplicable(ReasonNone)
				}
				for i := len(ds.days) - 1; i >= 0; i-- {
					if ds.days[i] < 0 {
						return Int(-ds.days[i])
					}
				}
				return Int(0)
			})
		}})
}
