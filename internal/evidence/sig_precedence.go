package evidence

func init() {
	register(Signal{Unsure: map[string][]string{"none": nil}, Name: "precedence", Version: 1, Type: TypeEnum, Values: precedenceValues, Needs: []string{"headers"}, Causal: true,
		Group: GroupHeaders, Doc: "The Precedence header (bulk, list, junk; any other value is other): mail sent to many by a machine.",
		Extract: func(in *Input) Value {
			return headerEnum(in, hdrPrecedence, func(v string) string { return inVocabulary(firstToken(v), precedenceValues[1:4]) })
		}})
}
