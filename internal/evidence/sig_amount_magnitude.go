package evidence

import "math"

func init() {
	register(Signal{Data: textData(), Bound: AtLeast, Name: "amount_magnitude", Version: 2, Type: TypeInt, Causal: true, Group: GroupContent,
		Settings: []string{settingMinLetters, settingLangChars},
		Doc:      "The order of magnitude (floor of log10) of the largest amount (in absolute value: a credit or a refund counts too) read unambiguously with the separators of the language and its regions; not applicable without one (an amount next to a currency code whose value is a plain integer is never read unambiguously: the code may be a word).",
		Extract: func(in *Input) Value {
			best := -1.0
			for _, a := range in.Amounts() {
				if a.Known && math.Abs(a.Value) > best {
					best = math.Abs(a.Value)
				}
			}
			if best < 1 {
				return NotApplicable(ReasonNone)
			}
			return Int(int(math.Floor(math.Log10(best)))).Cut(in.BodyCut())
		}})
}
