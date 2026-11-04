package evidence

func init() {
	register(Signal{Name: "inline_count", Version: 1, Type: TypeInt, Needs: []string{"attachments"}, Causal: true, Group: GroupContent,
		Doc:     "Parts shown inside the body (images of a signature or a newsletter, a calendar part).",
		Extract: func(in *Input) Value { return Int(len(in.Msg.Attachments) - len(realAttachments(in.Msg))) }})
}
