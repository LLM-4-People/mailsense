package evidence

func init() {
	register(Signal{Summary: true, Name: "read_state", Version: 1, Type: TypeBool, Needs: []string{"read_state"}, Group: GroupOwner,
		Doc:     "The owner has read the message. NOT causal: the owner's later action.",
		Extract: func(in *Input) Value { return Bool(in.Msg.IsRead) }})
}
