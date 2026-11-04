package evidence

func init() {
	register(Signal{Name: "recipient_count", Version: 1, Type: TypeInt, Needs: []string{"recipients"}, Causal: true, Group: GroupRecipients,
		Doc: "To and Cc recipients (Bcc is invisible to the others).",
		Extract: func(in *Input) Value {
			to, cc := toCc(in.Msg)
			return Int(to + cc)
		}})
}
