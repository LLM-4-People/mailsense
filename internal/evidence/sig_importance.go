package evidence

import "strings"

func init() {
	register(Signal{Name: "importance", Version: 2, Type: TypeEnum, Values: importanceValues, Needs: []string{"headers"}, Causal: true,
		Group: GroupHeaders,
		Doc:   "The importance the sender set, read from the transport headers as they arrived (MS-OXCMAIL 2.2.3.2.5): the Importance header (RFC 2156: low, normal, high; any other value: other), else X-Priority (1-2 high, 3 normal, 4-5 low); not applicable when neither says (partial when the header block was cut). The message's importance property is not read: the owner can change it on a received message (importance_mark).",
		Extract: func(in *Input) Value {
			vals, missing, ok := in.Header(hdrImportance)
			if !ok {
				return missing
			}
			if len(vals) > 0 {
				return Str(inVocabulary(firstToken(vals[0]), importanceValues))
			}
			if p, _, _ := in.Header(hdrPriority); len(p) > 0 {
				switch strings.TrimSpace(firstToken(p[0])) {
				case "1", "2":
					return Str("high")
				case "3":
					return Str("normal")
				case "4", "5":
					return Str("low")
				}
				return Str("other")
			}
			return NotApplicable(ReasonNone).Cut(in.HeadersCut())
		}})
}
