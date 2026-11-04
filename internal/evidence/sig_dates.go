package evidence

func init() {
	register(Signal{Data: textData(), Bound: AtLeast, Name: "date_days", Version: 3, Type: TypeInts, Causal: true, Group: GroupContent, Settings: dateSettings,
		Doc:     "Every full date the message's own text names that resolves to a civil date, as days from the message's day in the owner's zone (negative: before it): numeric and named dates of the CLDR patterns of its language and the language's regions (and ISO 8601), placed from the author's day (the Date header's offset) — a year-less one in the nearest year, a year in the language's calendar nearest it (converted when exact: Buddhist era, Persian arithmetic) —; the nearest evidence.dates.max, sorted. Ambiguous and unconvertible ones are date_unresolved; weekdays named alone are weekday_days.",
		Extract: func(in *Input) Value { return datesOf(in, false, func(d dates) Value { return Ints(d.days) }) }})
}
