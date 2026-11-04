package taxonomy

import (
	"maps"
	"slices"

	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

func validateThresholds(doc, questions, roles jsonx.Object, p *problems) {
	thresholds, ok := objectOf(doc, "thresholds")
	if !ok {
		p.add("thresholds: must be an object")
		return
	}
	known := knownThresholds()
	for _, k := range thresholds.Keys() {
		if !slices.Contains(known, Threshold(k)) {
			p.add("thresholds: unknown threshold %q (known: %v)", k, known)
		}
	}
	for _, k := range unitThresholds {
		if v, ok := numOf(get(thresholds, string(k))); !ok || v < 0 || v > 1 {
			p.add("thresholds/%s: must be a number in [0, 1], got %s", k, show(get(thresholds, string(k))))
		}
	}
	for _, k := range slices.Sorted(maps.Keys(levelThresholds)) {
		top, hasTop := levelMax(levelThresholds[k], questions, roles)
		v, ok := jsonx.Int(get(thresholds, string(k)))
		if !ok || (hasTop && (v < 0 || v > int64(top))) {
			p.add("thresholds/%s: must be an integer in [0, %d], got %s", k, top, show(get(thresholds, string(k))))
		}
	}
	for _, k := range slices.Sorted(maps.Keys(rangeThresholds)) {
		top, hasTop := levelMax(rangeThresholds[k], questions, roles)
		v, ok := numOf(get(thresholds, string(k)))
		if !ok || (hasTop && (v < 0 || v > float64(top))) {
			p.add("thresholds/%s: must be a number in [0, %d], got %s", k, top, show(get(thresholds, string(k))))
		}
	}
	neg, _ := numOf(get(thresholds, string(ThresholdSentimentNegative)))
	if pos, ok := numOf(get(thresholds, string(ThresholdSentimentPositive))); ok && pos < neg {
		p.add("thresholds/%s: must be ≥ %s", ThresholdSentimentPositive, ThresholdSentimentNegative)
	}
}

func knownThresholds() []Threshold {
	out := append(append(append([]Threshold(nil), unitThresholds...), slices.Collect(maps.Keys(levelThresholds))...),
		slices.Collect(maps.Keys(rangeThresholds))...)
	slices.Sort(out)
	return out
}

// levelMax is the top level of a role's scale. A dropped role keeps the
// built-in scale (a phantom 0..3 made a legitimate 4 an error in legacy).
func levelMax(role Role, questions, roles jsonx.Object) (int, bool) {
	for _, d := range []struct{ q, r jsonx.Object }{{questions, roles}, {mustObject(legacyScaleDoc(), "questions"), mustObject(legacyScaleDoc(), "roles")}} {
		if name, ok := get(d.r, string(role)).(string); ok {
			if spec, ok := get(d.q, name).(jsonx.Object); ok {
				if levels, ok := get(spec, "criteria").([]any); ok {
					return len(levels) - 1, true
				}
			}
		}
	}
	return 0, false
}

func mustObject(doc jsonx.Object, key string) jsonx.Object {
	o, _ := objectOf(doc, key)
	return o
}

func validateBlend(doc jsonx.Object, p *problems) {
	blend, ok := objectOf(doc, "prior_blend")
	if !ok {
		p.add("prior_blend: must be an object")
		return
	}
	for _, m := range blend {
		if !slices.Contains(BlendRoles, Role(m.Key)) {
			p.add("prior_blend: unknown role %q (blendable: %v)", m.Key, BlendRoles)
		} else if w, ok := numOf(m.Value); !ok || w < 0 || w > 1 {
			p.add("prior_blend/%s: must be a number in [0, 1], got %s", m.Key, show(m.Value))
		}
	}
}

func validateScalars(doc jsonx.Object, p *problems) {
	if n, ok := jsonx.Int(get(doc, "prior_min_samples")); !ok || n < 1 {
		p.add("prior_min_samples: must be an integer >= 1, got %s", show(get(doc, "prior_min_samples")))
	}
	if n, ok := jsonx.Int(get(doc, "body_chars")); !ok || n < 1 || n > MaxBodyChars {
		p.add("body_chars: must be an integer in [1, %d], got %s", MaxBodyChars, show(get(doc, "body_chars")))
	}
	if v, has := doc.Get("name"); has {
		if _, ok := v.(string); !ok {
			p.add("name: must be a string, got %s", show(v))
		}
	}
	tmpl, ok := get(doc, "state_template").(string)
	if !ok || tmpl == "" {
		p.add("state_template: must be a non-empty string")
		return
	}
	t, err := ParseTemplate(tmpl)
	if err != nil {
		p.add("state_template: invalid format string (%v)", err)
		return
	}
	for _, f := range t.Fields {
		if !slices.Contains(TemplateFields, f) {
			p.add("state_template: unknown placeholder {%s} (known: %v)", f, TemplateFields)
		}
	}
}

func validateContext(doc jsonx.Object, p *problems) {
	ctx, ok := objectOf(doc, "context")
	if !ok {
		p.add("context: must be an object")
		return
	}
	for _, k := range ctx.Keys() {
		if !slices.Contains(contextKeys, k) {
			p.add("context: unknown key %q (known: %v)", k, contextKeys)
		}
	}
	for _, k := range contextFlags {
		if _, ok := get(ctx, k).(bool); !ok {
			p.add("context/%s: must be true or false", k)
		}
	}
	if n, ok := jsonx.Int(get(ctx, "max_examples")); !ok || n < 0 {
		p.add("context/max_examples: must be an integer >= 0, got %s", show(get(ctx, "max_examples")))
	}
}

// validateEscalation checks each rule names a role and uses the field its
// question type needs; a rule of a dropped role is inert.
func validateEscalation(doc, questions, roles jsonx.Object, p *problems) {
	esc, ok := objectOf(doc, "escalation")
	if !ok {
		p.add("escalation: must be an object")
		return
	}
	for _, m := range esc {
		_, known := roleTypes[Role(m.Key)]
		rule, isObj := m.Value.(jsonx.Object)
		if !known || !isObj {
			p.add("escalation/%s: must be an object for a known role", m.Key)
			continue
		}
		target, _ := get(roles, m.Key).(string)
		spec, _ := get(questions, target).(jsonx.Object)
		if spec == nil {
			continue // dropped (or invalid: reported under roles)
		}
		for _, k := range rule.Keys() {
			v, ok := numOf(get(rule, k))
			switch {
			case k == "min_margin":
				p.add("escalation/%s: min_margin was replaced by min_confidence, the probability of the side the cutoff chooses (0.5 + the old margin at a 0.5 cutoff)", m.Key)
			case k != "min_confidence" || !ok || v < 0 || v > 1:
				p.add("escalation/%s: only min_confidence in [0, 1] applies to this role, got %s=%s", m.Key, k, show(get(rule, k)))
			}
		}
	}
}

// validateInsights checks the insight vocabulary names real options.
func validateInsights(doc, questions, roles jsonx.Object, p *problems) {
	ins, ok := objectOf(doc, "insights")
	if !ok {
		p.add("insights: must be an object")
		return
	}
	for _, k := range ins.Keys() {
		if !slices.Contains(insightKeys, k) {
			p.add("insights: unknown key %q (known: %v)", k, insightKeys)
		}
	}
	for _, kv := range []struct {
		key  string
		role Role
	}{{"suspicious_categories", RoleCategory}, {"known_relationships", RoleRelationship}} {
		key, role := kv.key, kv.role
		list, ok := get(ins, key).([]any)
		if !ok {
			p.add("insights/%s: must be a list of option names", key)
			continue
		}
		name, _ := get(roles, string(role)).(string)
		spec, _ := get(questions, name).(jsonx.Object)
		criteria, _ := get(spec, "criteria").(jsonx.Object)
		for _, v := range list {
			if s, ok := v.(string); !ok || (criteria != nil && !slices.Contains(criteria.Keys(), s)) {
				p.add("insights/%s: %s is not an option of the %s question", key, show(v), role)
			}
		}
	}
}
