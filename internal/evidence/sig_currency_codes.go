package evidence

import "slices"

// ambiguousCurrency stands for a symbol several currencies share ($, kr…).
const ambiguousCurrency = "ambiguous"

func init() {
	register(Signal{Data: textData(), Name: "currency_codes", Version: 1, Type: TypeSet, Causal: true, Group: GroupContent,
		Settings: []string{settingMinLetters, settingLangChars},
		Doc:      "The currencies of the amounts (ISO 4217 codes; \"ambiguous\" for a symbol several currencies share), distinct and sorted.",
		Extract: func(in *Input) Value {
			var out []string
			for _, a := range in.Amounts() {
				c := ambiguousCurrency
				if len(a.Codes) == 1 {
					c = a.Codes[0]
				}
				if !slices.Contains(out, c) {
					out = append(out, c)
				}
			}
			slices.Sort(out)
			return List(out).Cut(in.BodyCut())
		}})
}
