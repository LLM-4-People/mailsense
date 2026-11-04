package evidence

func init() {
	register(Signal{Data: textData(), Bound: AtMost, Name: "date_next_days", Version: 3, Type: TypeInt, Causal: true, Group: GroupContent, Settings: dateSettings,
		Doc: "Days to the nearest full date the own text names on or after the message's day (0: that same day); not applicable when it names none (the reason of an unresolved one when it names only such: ambiguous_order, calendar:<name>).",
		Extract: func(in *Input) Value {
			return datesOf(in, false, func(ds dates) Value {
				for _, d := range ds.days {
					if d >= 0 {
						return Int(d)
					}
				}
				if len(ds.days) == 0 && len(ds.unresolved) > 0 {
					return NotApplicable(ds.unresolved[0])
				}
				return NotApplicable(ReasonNone)
			})
		}})
}
