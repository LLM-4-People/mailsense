package evidence

import "strings"

func init() {
	register(Signal{Unsure: map[string][]string{"false": {"true"}}, Name: "spam_flag", Version: 1, Type: TypeBool, Needs: []string{"headers"}, Causal: true, Group: GroupHeaders,
		Doc: "A spam filter on the way flagged the message (X-Spam-Flag: YES, SpamAssassin's verdict header).",
		Extract: func(in *Input) Value {
			vals, missing, ok := in.Header(hdrSpamFlag)
			if !ok {
				return missing
			}
			for _, v := range vals {
				if strings.EqualFold(firstToken(v), "yes") {
					return Bool(true)
				}
			}
			return Bool(false).Cut(in.HeadersCut())
		}})
}
