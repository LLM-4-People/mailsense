package evidence

func init() {
	register(Signal{Data: textData(), Bound: AtLeast, Name: "date_unresolved", Version: 2, Type: TypeInt, Causal: true, Group: GroupContent,
		Settings: dateSettings,
		Doc:      "Full dates the message's own text names that have no exact civil date: a numeric date its language's regions read in different orders (03/04/2026, no field above 12), or one written in a calendar without an exact conversion (an Islamic, Hebrew, Coptic… year): dates, never guessed.",
		Extract: func(in *Input) Value {
			return datesOf(in, false, func(d dates) Value { return Int(len(d.unresolved)) })
		}})
}
