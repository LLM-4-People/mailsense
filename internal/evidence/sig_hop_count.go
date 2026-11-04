package evidence

func init() {
	register(Signal{Bound: AtLeast, Name: "hop_count", Version: 1, Type: TypeInt, Needs: []string{"headers"}, Causal: true, Group: GroupHeaders,
		Doc: "Received headers (RFC 5321 §4.4): the relays the message passed (a lower bound when the header block was cut).",
		Extract: func(in *Input) Value {
			vals, missing, ok := in.Header(hdrReceived)
			if !ok {
				return missing
			}
			return Int(len(vals)).Cut(in.HeadersCut())
		}})
}
