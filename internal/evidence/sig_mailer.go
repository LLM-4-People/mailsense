package evidence

func init() {
	register(Signal{Unsure: map[string][]string{"none": nil}, Name: "mailer", Version: 1, Type: TypeCategory, Opaque: true, Needs: []string{"headers"}, Causal: true, Group: GroupHeaders,
		Doc: "The sending software (X-Mailer, else User-Agent) as an opaque categorical value: a hash of its normalized name (lower case, version digits as one #); none without either header.",
		Extract: func(in *Input) Value {
			for _, h := range []string{hdrMailer, hdrUserAgent} {
				vals, missing, ok := in.Header(h)
				if !ok {
					return missing
				}
				if len(vals) > 0 && opaque(vals[0]) != "" {
					return Str(opaque(vals[0]))
				}
			}
			return Str("none").Cut(in.HeadersCut())
		}})
}
