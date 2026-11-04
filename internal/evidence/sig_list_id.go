package evidence

func init() {
	register(Signal{Unsure: map[string][]string{"false": {"true"}}, Name: "list_id", Version: 1, Type: TypeBool, Needs: []string{"headers"}, Causal: true, Group: GroupHeaders,
		Doc:     "A List-Id header (RFC 2919): the message came through a mailing list.",
		Extract: func(in *Input) Value { return in.Present(hdrListID) }})
}
