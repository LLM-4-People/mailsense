package evidence

func init() {
	register(Signal{Data: textData(), Name: "script", Version: 1, Type: TypeCategory, Causal: true, Group: GroupContent,
		Settings: []string{settingLangChars},
		Doc:      "The dominant Unicode script of the own text's letters (Latin, Han, Arabic…); none without letters.",
		Extract: func(in *Input) Value {
			if s := in.Language().Script; s != "" {
				return Str(s)
			}
			return Str("none")
		}})
}
