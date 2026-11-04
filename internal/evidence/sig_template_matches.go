package evidence

func init() {
	register(Signal{Name: "template_matches", Version: 1, Type: TypeInt, Causal: true, Context: true, Group: GroupContent,
		Settings: []string{settingTemplateCosine, settingTemplateWindow},
		Doc:      "Earlier messages of the same sender (the last evidence.template.window with a vector) whose embedding is a near-duplicate of this one (cosine ≥ evidence.template.min_cosine): mail cut from one template. Unavailable without a vector of the current space.",
		Extract: func(in *Input) Value {
			if in.env.tmpl == nil {
				return Missing(ReasonNoVector)
			}
			n, ok := in.env.tmpl.matches[in.Row]
			if !ok {
				return Missing(ReasonNoVector)
			}
			return Int(n)
		}})
}
