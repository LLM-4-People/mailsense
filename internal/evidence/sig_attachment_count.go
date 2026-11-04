package evidence

func init() {
	register(Signal{Name: "attachment_count", Version: 1, Type: TypeInt, Needs: []string{"attachments"}, Causal: true,
		Group: GroupContent, Doc: "Attachments shown as files (inline images of a signature or a body are not counted).",
		Extract: func(in *Input) Value { return Int(len(realAttachments(in.Msg))) }})
}
