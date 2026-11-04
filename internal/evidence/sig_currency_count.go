package evidence

func init() {
	register(Signal{Data: textData(), Bound: AtLeast, Name: "currency_count", Version: 1, Type: TypeInt, Causal: true, Group: GroupContent,
		Settings: []string{settingMinLetters, settingLangChars},
		Doc:      "Amounts of money in the message's own text: a number next to an ISO 4217 code or a CLDR currency symbol (locale.Amounts).",
		Extract:  func(in *Input) Value { return Int(len(in.Amounts())).Cut(in.BodyCut()) }})
}
