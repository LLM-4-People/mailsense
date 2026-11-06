package evidence

func init() {
	register(Signal{Name: "voting_options", Version: 1, Type: TypeBool, Causal: true, Group: GroupHeaders,
		Doc:     "The sender attached voting buttons (Message.VotingOptions is not empty): the message asks for a choice, so a verdict is being requested. An empty value treats absent and not-carried alike; the true direction is the one that carries meaning.",
		Extract: func(in *Input) Value { return Bool(in.Msg.VotingOptions != "") }})
}
