package evidence

func init() {
	register(Signal{Name: "has_calendar", Version: 1, Type: TypeBool, Needs: []string{"attachments"}, Causal: true, Group: GroupContent,
		Doc:     "The message carries iCalendar data (a calendar part with a METHOD, a text/calendar or application/ics attachment): an invitation, an answer or an event.",
		Extract: func(in *Input) Value { return Bool(hasCalendar(in.Msg)) }})
}
