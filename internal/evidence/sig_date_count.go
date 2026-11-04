package evidence

func init() {
	register(Signal{Data: textData(), Bound: AtLeast, Name: "date_count", Version: 3, Type: TypeInt, Causal: true, Group: GroupContent, Settings: dateSettings,
		Doc:     "How many full dates the message's own text names that resolve to a civil date (date_days, before the cap); the others are date_unresolved.",
		Extract: func(in *Input) Value { return datesOf(in, false, func(d dates) Value { return Int(d.all) }) }})
}
