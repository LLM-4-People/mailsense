package decision

import (
	"encoding/json"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

// openAI is the OpenAI Decisions wire format: questions and answers are
// arrays in question order, noul is "predicate" (criteria folded into the
// instructions with the provider's noul_fold), choices and levels are
// arrays, score answers carry no legend, any answer may be a refusal.
type openAI struct{}

const (
	openAIPredicate = "predicate"
	openAIRefusal   = "refusal"
)

func (openAI) encode(p *engine.Provider, state string, qs []engine.Question) ([]byte, error) {
	questions := make([]jsonx.Object, len(qs))
	for i := range qs {
		questions[i] = openAIQuestion(p, &qs[i])
	}
	return json.Marshal(jsonx.Object{{Key: "model", Value: p.Model}, {Key: "input", Value: state}, {Key: "questions", Value: questions}})
}

func openAIQuestion(p *engine.Provider, q *engine.Question) jsonx.Object {
	out := jsonx.Object{{Key: "type", Value: openAIType(q.Type)}, {Key: "name", Value: q.Name}, {Key: "instructions", Value: q.Instructions}}
	switch q.Type {
	case engine.Noul:
		if q.True != "" || q.False != "" {
			out[2].Value = strings.NewReplacer("{instructions}", q.Instructions, "{true}", q.True, "{false}", q.False).Replace(p.NoulFold)
		}
	case engine.Choice:
		choices := make([]jsonx.Object, len(q.Options))
		for i, o := range q.Options {
			choices[i] = jsonx.Object{{Key: "value", Value: o.Name}}
			if o.Description != nil {
				choices[i] = append(choices[i], jsonx.Member{Key: "description", Value: *o.Description})
			}
		}
		out = append(out, jsonx.Member{Key: "choices", Value: choices})
	case engine.Score:
		levels := make([]jsonx.Object, len(q.Levels))
		for i, l := range q.Levels {
			levels[i] = jsonx.Object{{Key: "label", Value: engine.LevelKey(i)}, {Key: "description", Value: l}}
		}
		out = append(out, jsonx.Member{Key: "levels", Value: levels})
	}
	return out
}

func openAIType(t engine.QuestionType) string {
	if t == engine.Noul {
		return openAIPredicate
	}
	return string(t)
}

func (openAI) decode(body []byte, qs []engine.Question) (decoded, error) {
	d := decoded{answers: map[string]engine.Answer{}}
	doc, err := topObject(body)
	if err != nil {
		return d, err
	}
	d.model = modelOf(doc)
	if err := usageOf(doc["usage"], &d); err != nil {
		return d, err
	}
	list, ok := doc["answers"].([]any)
	if !ok {
		return d, contractError("response", "answers is %s, not an array", jsonx.Kind(doc["answers"]))
	}
	if len(list) != len(qs) {
		return d, contractError("response", "%d answers for %d questions", len(list), len(qs))
	}
	for i := range qs {
		a, err := openAIAnswer(&qs[i], list[i])
		if err != nil {
			return d, err
		}
		d.answers[qs[i].Name] = a
	}
	return d, nil
}

func openAIAnswer(q *engine.Question, v any) (engine.Answer, error) {
	raw, ok := v.(map[string]any)
	if !ok {
		return engine.Answer{}, contractError(q.Name, "missing answer")
	}
	if name, has := raw["name"]; has && name != q.Name {
		return engine.Answer{}, contractError(q.Name, "answer out of order (named %v)", name)
	}
	t, _ := raw["type"].(string)
	if t == openAIRefusal {
		return engine.Answer{Type: q.Type, Refused: true}, nil
	}
	if t != openAIType(q.Type) {
		return engine.Answer{}, contractError(q.Name, "expected a %s answer, got %v", openAIType(q.Type), raw["type"])
	}
	a := engine.Answer{Type: q.Type}
	var err error
	switch q.Type {
	case engine.Noul:
		a.Value, err = probability(q.Name, "probability", raw["probability"])
		return a, err
	case engine.Choice:
		a.Choice, _ = raw["choice"].(string)
		if a.Probabilities, err = openAIProbabilities(q, raw["probabilities"], "value", optionKeys(q)); err == nil {
			err = checkChoice(q, &a)
		}
	case engine.Score:
		if a.Value, err = bounded(q.Name, "score", raw["score"], 0, float64(q.Top())); err == nil {
			a.Probabilities, err = openAIProbabilities(q, raw["probabilities"], "label", levelKeys(q))
		}
	}
	if err != nil {
		return a, err
	}
	a.Confidence, err = confidence(q.Name, raw["confidence"])
	return a, err
}

// openAIProbabilities reads [{<key>: …, probability: …}] (the score legend
// is rebuilt from the level labels we sent: "0".."n-1").
func openAIProbabilities(q *engine.Question, v any, keyField string, allowed map[string]bool) (map[string]float64, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, contractError(q.Name, "probabilities is %s, not an array", jsonx.Kind(v))
	}
	out := map[string]float64{}
	for _, item := range list {
		m, _ := item.(map[string]any)
		key, _ := m[keyField].(string)
		if err := putProbability(q, out, allowed, key, m["probability"]); err != nil {
			return nil, err
		}
	}
	return out, nil
}
