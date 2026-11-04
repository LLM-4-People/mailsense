package evidence

import (
	"strconv"
	"strings"
)

func init() {
	register(Signal{Name: "spam_scl", Version: 1, Type: TypeInt, Causal: true, Group: GroupHeaders,
		Doc: "The Exchange spam confidence level (-1 trusted, 0..9 from clean to spam): the message's SCL property, else its X-MS-Exchange-Organization-SCL header; not applicable when neither is there (partial when the header block was cut).",
		Extract: func(in *Input) Value {
			if in.Msg.SpamConfidence.Valid {
				return Int(int(in.Msg.SpamConfidence.N))
			}
			vals, missing, ok := in.Header(hdrSCL)
			if !ok {
				return missing
			}
			for _, v := range vals {
				if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
					return Int(n)
				}
			}
			return NotApplicable(ReasonNone).Cut(in.HeadersCut())
		}})
}
