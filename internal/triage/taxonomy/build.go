package taxonomy

import (
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

// maxProfileInt bounds the profile counts a document can set. They are batch
// sizes, never more than a few thousand; the bound keeps a hostile or corrupt
// document from narrowing an int64 into a negative int.
const maxProfileInt = 1<<31 - 1

// build turns a validated document into a Profile.
func build(doc jsonx.Object) (*Profile, error) {
	questions, _ := objectOf(doc, "questions")
	fp, err := Fingerprint(questions)
	if err != nil {
		return nil, err
	}
	p := &Profile{Name: "default", Roles: map[Role]string{}, Thresholds: map[Threshold]float64{}, PriorBlend: map[Role]float64{},
		Escalation: map[Role]EscalationRule{}, Fingerprint: fp, roleOf: map[string]Role{}}
	if name, ok := get(doc, "name").(string); ok {
		p.Name = name
	}
	bodyChars, _ := jsonx.Int(get(doc, "body_chars"))
	minSamples, _ := jsonx.Int(get(doc, "prior_min_samples"))
	if p.BodyChars, err = profileCount(bodyChars, "body_chars"); err != nil {
		return nil, err
	}
	if p.PriorMinSamples, err = profileCount(minSamples, "prior_min_samples"); err != nil {
		return nil, err
	}
	p.StateTemplate, _ = get(doc, "state_template").(string)
	p.template, _ = ParseTemplate(p.StateTemplate) // validated
	for _, m := range questions {
		p.Questions = append(p.Questions, question(m.Key, m.Value.(jsonx.Object)))
	}
	for _, m := range mustObject(doc, "roles") {
		p.Roles[Role(m.Key)] = m.Value.(string)
		p.roleOf[m.Value.(string)] = Role(m.Key)
	}
	for _, m := range mustObject(doc, "thresholds") {
		p.Thresholds[Threshold(m.Key)], _ = numOf(m.Value)
	}
	for _, m := range mustObject(doc, "prior_blend") {
		p.PriorBlend[Role(m.Key)], _ = numOf(m.Value)
	}
	if err := buildContext(p, mustObject(doc, "context")); err != nil {
		return nil, err
	}
	for _, m := range mustObject(doc, "escalation") {
		rule := m.Value.(jsonx.Object)
		var r EscalationRule
		if v, ok := numOf(get(rule, "min_confidence")); ok {
			r.MinConfidence = &v
		}
		p.Escalation[Role(m.Key)] = r
	}
	ins := mustObject(doc, "insights")
	p.SuspiciousCategories, p.KnownRelationships = stringList(get(ins, "suspicious_categories")), stringList(get(ins, "known_relationships"))
	return p, nil
}

func buildContext(p *Profile, ctx jsonx.Object) error {
	p.IncludeHistory, _ = get(ctx, "include_history").(bool)
	p.IncludeExamples, _ = get(ctx, "include_examples").(bool)
	p.IncludeFacts, _ = get(ctx, "include_facts").(bool)
	n, _ := jsonx.Int(get(ctx, "max_examples"))
	count, err := profileCount(n, "context/max_examples")
	if err != nil {
		return err
	}
	p.MaxExamples = count
	return nil
}

// profileCount narrows a document count to int, refusing a value outside
// [0, maxProfileInt]. Every profile count conversion goes through it, so no
// site can silently truncate an int64 into a negative int.
func profileCount(v int64, field string) (int, error) {
	if v < 0 || v > maxProfileInt {
		return 0, fmt.Errorf("%s must be in [0, %d]", field, maxProfileInt)
	}
	return int(v), nil
}

func stringList(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func question(name string, spec jsonx.Object) engine.Question {
	q := engine.Question{Name: name}
	t, _ := get(spec, "type").(string)
	q.Type = engine.QuestionType(t)
	q.Instructions, _ = get(spec, "instructions").(string)
	switch c := get(spec, "criteria").(type) {
	case jsonx.Object:
		for _, m := range c {
			if q.Type == engine.Noul {
				s, _ := m.Value.(string)
				if m.Key == "true" {
					q.True = s
				} else {
					q.False = s
				}
				continue
			}
			o := engine.Option{Name: m.Key}
			if s, ok := m.Value.(string); ok {
				o.Description = &s
			}
			q.Options = append(q.Options, o)
		}
	case []any:
		for _, l := range c {
			s, _ := l.(string)
			q.Levels = append(q.Levels, s)
		}
	}
	return q
}
