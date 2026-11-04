package evidence

import "strings"

func init() {
	register(Signal{Unsure: map[string][]string{"none": {"present", "one_click"}, "present": {"one_click"}}, Name: "list_unsubscribe", Version: 1, Type: TypeEnum, Values: []string{"none", "present", "one_click"},
		Needs: []string{"headers"}, Causal: true, Group: GroupHeaders,
		Doc: "A List-Unsubscribe header (RFC 2369): none, present, or one_click when List-Unsubscribe-Post offers RFC 8058 one-click unsubscription (bulk senders must).",
		Extract: func(in *Input) Value {
			vals, missing, ok := in.Header(hdrListUnsubscribe)
			if !ok {
				return missing
			}
			if len(vals) == 0 {
				return Str("none").Cut(in.HeadersCut())
			}
			post, _, _ := in.Header(hdrListUnsubPost)
			for _, p := range post {
				if strings.EqualFold(strings.TrimSpace(p), oneClick) {
					return Str("one_click")
				}
			}
			return Str("present").Cut(in.HeadersCut())
		}})
}
