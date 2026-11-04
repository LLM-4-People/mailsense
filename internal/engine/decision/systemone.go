package decision

import (
	"encoding/json"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

// systemone is the Ondelle / Example provider Zelvane / Cloudflare Aubelle wire format:
// questions and answers are objects keyed by question name.
type systemone struct{}

func (systemone) encode(p *engine.Provider, state string, qs []engine.Question) ([]byte, error) {
	questions := jsonx.Object{}
	for i := range qs {
		questions = append(questions, jsonx.Member{Key: qs[i].Name, Value: systemoneQuestion(&qs[i])})
	}
	return json.Marshal(jsonx.Object{{Key: "model", Value: p.Model}, {Key: "state", Value: state}, {Key: "questions", Value: questions}})
}

func systemoneQuestion(q *engine.Question) jsonx.Object {
	out := jsonx.Object{{Key: "type", Value: string(q.Type)}, {Key: "instructions", Value: q.Instructions}}
	switch q.Type {
	case engine.Noul:
		criteria := jsonx.Object{}
		if q.True != "" {
			criteria = append(criteria, jsonx.Member{Key: "true", Value: q.True})
		}
		if q.False != "" {
			criteria = append(criteria, jsonx.Member{Key: "false", Value: q.False})
		}
		if len(criteria) > 0 {
			out = append(out, jsonx.Member{Key: "criteria", Value: criteria})
		}
	case engine.Choice:
		criteria := jsonx.Object{}
		for _, o := range q.Options {
			var desc any
			if o.Description != nil {
				desc = *o.Description
			}
			criteria = append(criteria, jsonx.Member{Key: o.Name, Value: desc})
		}
		out = append(out, jsonx.Member{Key: "criteria", Value: criteria})
	case engine.Score:
		out = append(out, jsonx.Member{Key: "criteria", Value: q.Levels})
	}
	return out
}

func (systemone) decode(body []byte, qs []engine.Question) (decoded, error) {
	d := decoded{answers: map[string]engine.Answer{}}
	doc, err := topObject(body)
	if err != nil {
		return d, err
	}
	d.model = modelOf(doc)
	if err := usageOf(doc["usage"], &d); err != nil {
		return d, err
	}
	if err := billingOf(doc, &d); err != nil {
		return d, err
	}
	answers, ok := doc["answers"].(map[string]any)
	if !ok {
		return d, contractError("response", "answers is %s, not an object", jsonx.Kind(doc["answers"]))
	}
	index, err := byName(qs)
	if err != nil {
		return d, err
	}
	for name := range answers {
		if index[name] == nil {
			return d, contractError(name, "answer for a question that was not asked")
		}
	}
	for i := range qs {
		a, err := systemoneAnswer(&qs[i], answers[qs[i].Name])
		if err != nil {
			return d, err
		}
		d.answers[qs[i].Name] = a
	}
	return d, nil
}

func systemoneAnswer(q *engine.Question, v any) (engine.Answer, error) {
	raw, ok := v.(map[string]any)
	if !ok {
		return engine.Answer{}, contractError(q.Name, "missing answer")
	}
	if t, _ := raw["type"].(string); t != string(q.Type) {
		return engine.Answer{}, contractError(q.Name, "expected a %s answer, got %v", q.Type, raw["type"])
	}
	a := engine.Answer{Type: q.Type}
	var err error
	switch q.Type {
	case engine.Noul:
		a.Value, err = probability(q.Name, "noul", raw["noul"])
		return a, err
	case engine.Choice:
		a.Choice, _ = raw["choice"].(string)
		if a.Probabilities, err = probabilityMap(q, raw["probabilities"], optionKeys(q)); err == nil {
			err = checkChoice(q, &a)
		}
	case engine.Score:
		if a.Value, err = bounded(q.Name, "score", raw["score"], 0, float64(q.Top())); err == nil {
			a.Probabilities, err = probabilityMap(q, raw["probabilities"], levelKeys(q))
		}
	}
	if err != nil {
		return a, err
	}
	a.Confidence, err = confidence(q.Name, raw["confidence"])
	return a, err
}

func probabilityMap(q *engine.Question, v any, allowed map[string]bool) (map[string]float64, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, contractError(q.Name, "probabilities is %s, not an object", jsonx.Kind(v))
	}
	out := map[string]float64{}
	for k, p := range m {
		if err := putProbability(q, out, allowed, k, p); err != nil {
			return nil, err
		}
	}
	return out, nil
}
