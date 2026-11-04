package evidence

func init() {
	register(Signal{Data: textData(), Bound: AtLeast, Name: "time_count", Version: 1, Type: TypeInt, Causal: true, Group: GroupContent,
		Settings: []string{settingMinLetters, settingLangChars},
		Doc:      "Times of day the own text names (its language's CLDR time patterns: 14:30, 2:30 PM, 午後2:30…).",
		Extract:  func(in *Input) Value { return Int(in.Mentions().Times).Cut(in.BodyCut()) }})
}
