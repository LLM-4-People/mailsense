package evidence

import "slices"

func init() {
	register(Signal{Name: "attachment_types", Version: 1, Type: TypeSet, Needs: []string{"attachments"}, Causal: true,
		Group: GroupContent, Doc: "The MIME types of the attachments shown as files (application/pdf, text/calendar…), distinct and sorted.",
		Extract: func(in *Input) Value {
			var out []string
			for _, a := range realAttachments(in.Msg) {
				if t := mediaType(a); t != "" && !slices.Contains(out, t) {
					out = append(out, t)
				}
			}
			slices.Sort(out)
			return List(out)
		}})
}
