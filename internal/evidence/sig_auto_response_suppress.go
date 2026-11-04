package evidence

func init() {
	register(Signal{Unsure: map[string][]string{"false": {"true"}}, Name: "auto_response_suppress", Version: 1, Type: TypeBool, Needs: []string{"headers"}, Causal: true,
		Group: GroupHeaders, Doc: "An X-Auto-Response-Suppress header (MS-OXCMAIL): the sender asked that no automatic reply answer it — machine-sent mail sets it.",
		Extract: func(in *Input) Value { return in.Present(hdrAutoSuppress) }})
}
