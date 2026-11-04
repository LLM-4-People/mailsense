package evidence

func init() {
	register(Signal{Data: textData(), Bound: AtLeast, Name: "weekday_days", Version: 3, Type: TypeInts, Causal: true, Group: GroupContent, Settings: dateSettings,
		Doc:     "The weekdays the own text names alone (wide CLDR names outside a date), each at its next occurrence from the author's day (the Date header's offset: the weekday the author meant), as days from the message's day in the owner's zone, sorted.",
		Extract: func(in *Input) Value { return datesOf(in, true, func(d dates) Value { return Ints(d.days) }) }})
}
