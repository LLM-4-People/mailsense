package taxonomy

import (
	"fmt"
	"maps"
	"slices"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

// problems collects every validation message.
type problems []string

func (p *problems) add(format string, args ...any) { *p = append(*p, fmt.Sprintf(format, args...)) }

// validate returns every problem of a merged profile document
// (profile.py validate_document, plus unknown keys: PORTING §10).
func validate(doc jsonx.Object) []string {
	var p problems
	for _, k := range doc.Keys() {
		if !slices.Contains(knownKeys, k) {
			p.add("profile: unknown key %q (known: %v)", k, knownKeys)
		}
	}
	if v, ok := jsonx.Int(get(doc, "version")); !ok || v != Version {
		p.add("profile: version must be %d, got %s", Version, show(get(doc, "version")))
	}
	questions := validateQuestions(doc, &p)
	roles := validateRoles(doc, questions, &p)
	validateThresholds(doc, questions, roles, &p)
	validateBlend(doc, &p)
	validateScalars(doc, &p)
	validateContext(doc, &p)
	validateEscalation(doc, questions, roles, &p)
	validateInsights(doc, questions, roles, &p)
	return p
}

func validateQuestions(doc jsonx.Object, p *problems) jsonx.Object {
	questions, ok := objectOf(doc, "questions")
	if !ok || len(questions) == 0 {
		p.add("questions: must be a non-empty object")
		return jsonx.Object{}
	}
	for _, m := range questions {
		if m.Key == "" || len([]rune(m.Key)) > MaxQuestionName {
			p.add("question names must be non-empty strings up to %d chars, got %q", MaxQuestionName, m.Key)
			continue
		}
		validateQuestion(m.Key, m.Value, p)
	}
	return questions
}

// validateQuestion checks the JSON shape here and the question contract
// once, in engine.Question.Validate.
func validateQuestion(name string, v any, p *problems) {
	spec, ok := v.(jsonx.Object)
	if !ok {
		p.add("questions/%s: must be an object", name)
		return
	}
	for _, k := range spec.Keys() {
		if k != "type" && k != "instructions" && k != "criteria" {
			p.add("questions/%s: unknown key %q (known: type, instructions, criteria)", name, k)
		}
	}
	qtype, _ := get(spec, "type").(string)
	if qtype != string(engine.Noul) && qtype != string(engine.Choice) && qtype != string(engine.Score) {
		p.add("questions/%s: type must be one of [choice noul score], got %s", name, show(get(spec, "type")))
		return
	}
	if _, ok := get(spec, "instructions").(string); !ok {
		p.add("questions/%s: instructions must be a string", name)
		return
	}
	if !criteriaShape(name, engine.QuestionType(qtype), get(spec, "criteria"), p) {
		return
	}
	q := question(name, spec)
	if err := q.Validate(); err != nil {
		p.add("questions/%s: %v", name, err)
	}
}

// criteriaShape checks the JSON types of criteria (counts and contents are
// the engine's contract).
func criteriaShape(name string, qtype engine.QuestionType, criteria any, p *problems) bool {
	switch qtype {
	case engine.Choice:
		c, ok := criteria.(jsonx.Object)
		if !ok || slices.ContainsFunc(c, func(m jsonx.Member) bool { _, s := m.Value.(string); return m.Value != nil && !s }) {
			p.add("questions/%s: choice criteria must be an object of option descriptions (strings or null)", name)
			return false
		}
	case engine.Score:
		levels, ok := criteria.([]any)
		if !ok || slices.ContainsFunc(levels, func(l any) bool { _, ok := l.(string); return !ok }) {
			p.add("questions/%s: score criteria must be an array of level labels", name)
			return false
		}
	default: // noul
		c, ok := criteria.(jsonx.Object)
		if criteria != nil && !ok {
			p.add("questions/%s: noul criteria must be an object when given", name)
			return false
		}
		for _, m := range c {
			if _, s := m.Value.(string); (m.Key != "true" && m.Key != "false") || !s {
				p.add("questions/%s: noul criteria keys must be 'true'/'false' with string values, got %q", name, m.Key)
				return false
			}
		}
	}
	return true
}

// validateRoles checks every role names a question of its type and that
// no question plays two roles (blending would then depend on map order).
func validateRoles(doc jsonx.Object, questions jsonx.Object, p *problems) jsonx.Object {
	roles, ok := objectOf(doc, "roles")
	if !ok {
		p.add("roles: must be an object")
		return jsonx.Object{}
	}
	played := map[string]string{}
	for _, m := range roles {
		want, known := roleTypes[Role(m.Key)]
		if !known {
			p.add("roles: unknown role %q (known: %v)", m.Key, slices.Sorted(maps.Keys(roleTypes)))
			continue
		}
		target, isName := m.Value.(string)
		spec, _ := get(questions, target).(jsonx.Object)
		switch {
		case !isName || spec == nil:
			p.add("roles/%s: %s is not a defined question (set the role to null to drop that section)", m.Key, show(m.Value))
		case get(spec, "type") != string(want):
			p.add("roles/%s: question %q must be a %s question", m.Key, target, want)
		case played[target] != "":
			p.add("roles/%s: question %q already plays role %s (one role per question)", m.Key, target, played[target])
		default:
			played[target] = m.Key
		}
	}
	return roles
}
