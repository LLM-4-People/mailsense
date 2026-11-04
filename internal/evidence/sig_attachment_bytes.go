package evidence

func init() {
	register(Signal{Bound: AtLeast, Name: "attachment_bytes", Version: 1, Type: TypeInt, Needs: []string{"attachments"}, Causal: true,
		Group: GroupContent, Doc: "Total decoded size of the attachments shown as files (a lower bound when a size is unknown).",
		Extract: func(in *Input) Value {
			total, unknown := int64(0), false
			for _, a := range realAttachments(in.Msg) {
				if a.Size < 0 {
					unknown = true
					continue
				}
				total += a.Size
			}
			return Num(float64(total)).Cut(unknown)
		}})
}
