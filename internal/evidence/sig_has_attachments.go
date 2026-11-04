package evidence

func init() {
	register(Signal{Summary: true, Name: "has_attachments", Version: 1, Type: TypeBool, Causal: true, Group: GroupContent,
		Doc: "The message has attachments (the source's flag, or attachment metadata).",
		Extract: func(in *Input) Value {
			return Bool(in.Msg.HasAttachments || len(in.Msg.Attachments) > 0)
		}})
}
