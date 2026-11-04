package evidence

func init() {
	register(Signal{Summary: true, Name: "folder_path", Version: 1, Type: TypeCategory, Opaque: true, Group: GroupOwner,
		Doc:     "The folder the message is filed in, as an opaque categorical value (the owner's own taxonomy, hashed). NOT causal.",
		Extract: func(in *Input) Value { return Str(opaque(in.Msg.FolderPath)) }})
}
