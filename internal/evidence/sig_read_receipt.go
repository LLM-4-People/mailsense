package evidence

func init() {
	register(Signal{Unsure: map[string][]string{"false": {"true"}}, Name: "read_receipt", Version: 1, Type: TypeBool, Causal: true, Group: GroupHeaders,
		Doc: "The sender asked for a read receipt: the message's flag, or a Disposition-Notification-To header (RFC 8098).",
		Extract: func(in *Input) Value {
			if in.Msg.ReadReceiptRequested {
				return Bool(true)
			}
			return in.Present(hdrNotificationTo) // without a header block an unset flag tells nothing: unavailable
		}})
}
